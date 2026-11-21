// Writer serializes a binpazer file to a Sink, byte-identical to the C reference.

import { encodeEnvelopeHeader } from "./envelope.ts";
import {
	BLOCK_TYPE_TABLE_GUID,
	BinpazerError,
	CODEC_BY_GUID,
	CODEC_INVALID,
	FLAG_COMPRESSED,
	FLAG_CRITICAL,
	FLAG_HAS_CRC,
	FLAG_LARGE_MODE,
	END_GUID,
	FOOTER_MAGIC,
	HEADER_FLAG_APPENDABLE,
	IO_BUF_SIZE,
	LARGE_UNIT,
	MAGIC,
	TYPE_BLOCK_INDEX,
	TYPE_BLOCK_TYPE_TABLE,
	TYPE_END,
	TYPE_PADDING,
	VERSION_MAJOR,
	VERSION_MINOR,
	VERSION_MINOR_APPENDABLE,
	VERSION_MINOR_COMPRESSION,
	align8,
	checkGuid,
	checkU16,
	crc32c,
	putU16,
	putU32,
	putU64,
} from "./format.ts";
import type { Guid, IndexEntry, Sink, Source, TypeDef, TypeEntry } from "./format.ts";
import { Reader } from "./reader.ts";
import type { ReaderOptions } from "./reader.ts";

const encoder = new TextEncoder();
const ZEROS = new Uint8Array(8);
const PAD_ZEROS = new Uint8Array(4096);
const END_TYPE_NAME = "End";
// MAX_PAD_EXTENT caps one resume Padding block, as C and Go do.
const MAX_PAD_EXTENT = 1 << 20;

export interface WriterOptions {
	versionMinor?: number;
	appendable?: boolean;
}

interface ResumeState {
	offset: number;
	versionMinor: number;
	tail: number;
}

// resuming hands ResumeAppendWriter's state to the constructor.
let resuming: ResumeState | null = null;

export class Writer {
	private readonly sink: Sink;
	private readonly buf = new Uint8Array(IO_BUF_SIZE);
	private bufLen = 0;
	private off = 0;
	private err: BinpazerError | null = null;
	private readonly tracked: IndexEntry[] = [];
	private indexOffset = 0;
	private wroteIndex = false;
	private ended = false;
	private readonly versionMinor: number;
	private usedCompression = false;
	private readonly appendable: boolean;
	private sealed = 0;

	constructor(sink: Sink, writerGuid: Guid, writerName: string, defs: TypeDef[], opts?: WriterOptions) {
		this.sink = sink;
		const resume = resuming;
		if (resume !== null) {
			resuming = null;
			this.appendable = true;
			this.versionMinor = resume.versionMinor;
			this.off = resume.offset;
			this.sealed = resume.tail;
			this.padOver(resume.tail);
			this.flush();
			return;
		}
		this.appendable = opts?.appendable === true;
		this.versionMinor = this.appendable ? VERSION_MINOR_APPENDABLE : (opts?.versionMinor ?? VERSION_MINOR);
		checkU16(this.versionMinor, "version_minor");
		checkGuid(writerGuid, "writer GUID");
		const name = encoder.encode(writerName);
		const btName = encoder.encode("BlockTypeTable");
		const defNames = defs.map((d) => {
			checkU16(d.typeId, "type id");
			checkGuid(d.guid, "type GUID");
			return encoder.encode(d.name);
		});

		const firstBlock = align8(56 + 4 + name.length + 1);
		const hdr = new Uint8Array(56);
		for (let i = 0; i < 8; i++) {
			hdr[i] = MAGIC.charCodeAt(i);
		}
		hdr.set([0x0d, 0x0a, 0x1a, 0x0a], 8);
		putU16(hdr, 12, VERSION_MAJOR);
		putU16(hdr, 14, this.versionMinor);
		hdr.set(writerGuid, 16);
		hdr.fill(0xff, 32, 40);
		putU64(hdr, 40, firstBlock);
		if (this.appendable) {
			putU32(hdr, 48, HEADER_FLAG_APPENDABLE);
		}
		this.write(hdr);
		this.writeString(name);
		this.pad(firstBlock - this.off);

		let payloadLen = 8 + bttEntrySize(btName.length);
		for (const n of defNames) {
			payloadLen += bttEntrySize(n.length);
		}
		const endName = encoder.encode(END_TYPE_NAME);
		if (this.appendable) {
			payloadLen += bttEntrySize(endName.length);
		}
		if (payloadLen > 0xffffffff) {
			this.fail(new BinpazerError("range", "block type table too large"));
		}
		const bh = new Uint8Array(16);
		putU16(bh, 0, TYPE_BLOCK_TYPE_TABLE);
		putU16(bh, 2, FLAG_CRITICAL);
		putU32(bh, 4, payloadLen);
		putU32(bh, 8, defs.length + (this.appendable ? 2 : 1));
		this.write(bh);
		this.writeBTTEntry(TYPE_BLOCK_TYPE_TABLE, BLOCK_TYPE_TABLE_GUID, btName);
		defs.forEach((d, i) => this.writeBTTEntry(d.typeId, d.guid, defNames[i]));
		if (this.appendable) {
			this.writeBTTEntry(TYPE_END, END_GUID, endName);
		}
		this.pad(align8(this.off) - this.off);
		if (this.appendable) {
			this.flush();
		}
	}

	// sealedTail is the count of stale bytes ResumeAppendWriter covered with Padding blocks.
	get sealedTail(): number {
		return this.sealed;
	}

	// offset is the file offset where the next block starts.
	get offset(): number {
		return this.off;
	}

	block(typeId: number, flags: number, payload: Uint8Array): void {
		this.check();
		this.writeBlock(typeId, flags, null, payload);
	}

	// blockCompressed wraps bytes the caller already encoded with codecId in an envelope.
	blockCompressed(typeId: number, flags: number, codecId: number, codecGuid: Guid | null, stored: Uint8Array, uncompressedLength: number): void {
		this.check();
		if (codecId === CODEC_INVALID || (codecId === CODEC_BY_GUID && codecGuid === null)) {
			this.fail(new BinpazerError("range", `codec id ${codecId} needs ${codecId === CODEC_INVALID ? "a valid id" : "a codec GUID"}`));
		}
		this.checkCanCompress();
		let env: Uint8Array;
		try {
			env = encodeEnvelopeHeader(codecId, codecGuid, uncompressedLength);
		} catch (e) {
			this.fail(e instanceof BinpazerError ? e : new BinpazerError("range", String(e)));
		}
		this.writeBlock(typeId, flags | FLAG_COMPRESSED, env, stored);
		this.usedCompression = true;
	}

	// checkCanCompress refuses a compressed block a streaming writer cannot declare.
	checkCanCompress(): void {
		this.check();
		if (this.sink.seek === undefined && this.versionMinor < VERSION_MINOR_COMPRESSION) {
			this.fail(new BinpazerError("noseek", "a compressed block on a non-seekable sink needs versionMinor >= 1 declared up front"));
		}
	}

	// writeIndex with no argument indexes every block this writer wrote, like Go's WriteIndex.
	writeIndex(entries?: IndexEntry[]): void {
		this.check();
		if (this.appendable) {
			throw new BinpazerError("range", "an appendable file carries no Block Index");
		}
		const list = entries ?? this.tracked;
		const payloadLen = 8 + list.length * 16;
		if (payloadLen > 0xffffffff) {
			this.fail(new BinpazerError("range", "block index too large"));
		}
		const body = new Uint8Array(16 + list.length * 16);
		putU16(body, 0, TYPE_BLOCK_INDEX);
		putU32(body, 4, payloadLen);
		putU32(body, 8, list.length);
		list.forEach((e, i) => {
			checkU16(e.typeId, "index type id");
			if (!Number.isSafeInteger(e.offset) || e.offset < 0) {
				this.fail(new BinpazerError("range", `index offset ${e.offset} is invalid`));
			}
			putU16(body, 16 + i * 16, e.typeId);
			putU64(body, 16 + i * 16 + 8, e.offset);
		});
		this.indexOffset = this.off;
		this.write(body);
		this.pad(align8(this.off) - this.off);
		this.wroteIndex = true;
	}

	// end writes the footer, flushes, and back-patches the header when the sink can seek.
	end(): void {
		if (this.err !== null) {
			throw this.err;
		}
		if (this.appendable && this.ended) {
			return;
		}
		this.check();
		if (this.appendable) {
			this.writeBlock(TYPE_END, 0, null, new Uint8Array(0));
		}
		if (this.wroteIndex) {
			const ft = new Uint8Array(16);
			putU64(ft, 0, this.indexOffset);
			for (let i = 0; i < 8; i++) {
				ft[8 + i] = FOOTER_MAGIC.charCodeAt(i);
			}
			this.write(ft);
		}
		this.flush();
		const seek = this.sink.seek;
		if (seek !== undefined) {
			this.guard(() => {
				if (this.usedCompression && this.versionMinor < VERSION_MINOR_COMPRESSION) {
					seek.call(this.sink, 14);
					const v = new Uint8Array(2);
					putU16(v, 0, VERSION_MINOR_COMPRESSION);
					this.sink.write(v);
				}
				seek.call(this.sink, 32);
				const b = new Uint8Array(8);
				putU64(b, 0, this.off);
				this.sink.write(b);
			});
		}
		this.ended = true;
	}

	private writeBlock(typeId: number, flags: number, prefix: Uint8Array | null, body: Uint8Array): void {
		checkU16(typeId, "type id");
		checkU16(flags, "flags");
		if (this.appendable) {
			flags |= FLAG_HAS_CRC;
		}
		const payloadLen = (prefix === null ? 0 : prefix.length) + body.length;
		let lengthField: number;
		if (flags & FLAG_LARGE_MODE) {
			if (payloadLen % LARGE_UNIT !== 0) {
				this.fail(new BinpazerError("range", "a large_mode payload must be a multiple of 4096 bytes"));
			}
			lengthField = payloadLen / LARGE_UNIT;
		} else {
			lengthField = payloadLen;
		}
		if (lengthField > 0xffffffff) {
			this.fail(new BinpazerError("range", "payload too large for the length field (use FLAG_LARGE_MODE)"));
		}

		this.tracked.push({ typeId, offset: this.off });
		const bh = new Uint8Array(8);
		putU16(bh, 0, typeId);
		putU16(bh, 2, flags);
		putU32(bh, 4, lengthField);
		this.write(bh);
		if (prefix !== null) {
			this.write(prefix);
		}
		this.write(body);
		if (flags & FLAG_HAS_CRC) {
			let crc = crc32c(0, bh);
			if (prefix !== null) {
				crc = crc32c(crc, prefix);
			}
			crc = crc32c(crc, body);
			const cb = new Uint8Array(4);
			putU32(cb, 0, crc);
			this.write(cb);
		}
		this.pad(align8(this.off) - this.off);
	}

	private writeBTTEntry(typeId: number, guid: Guid, name: Uint8Array): void {
		const hdr = new Uint8Array(20);
		putU16(hdr, 0, typeId);
		hdr.set(guid, 4);
		this.write(hdr);
		this.writeString(name);
		const raw = 25 + name.length;
		this.pad(align8(raw) - raw);
	}

	private writeString(s: Uint8Array): void {
		const lp = new Uint8Array(4);
		putU32(lp, 0, s.length);
		this.write(lp);
		this.write(s);
		this.write(ZEROS.subarray(0, 1));
	}

	private pad(n: number): void {
		this.write(ZEROS.subarray(0, n));
	}

	private write(src: Uint8Array): void {
		const n = src.length;
		if (n === 0) {
			return;
		}
		if (n > this.buf.length - this.bufLen) {
			this.flush();
			if (n >= this.buf.length) {
				this.guard(() => this.sink.write(src));
				this.off += n;
				return;
			}
		}
		this.buf.set(src, this.bufLen);
		this.bufLen += n;
		this.off += n;
	}

	private flush(): void {
		if (this.bufLen === 0) {
			return;
		}
		const chunk = this.buf.subarray(0, this.bufLen);
		this.guard(() => this.sink.write(chunk));
		this.bufLen = 0;
	}

	// guard turns a sink exception into a sticky "io" error.
	private guard(f: () => void): void {
		try {
			f();
		} catch (e) {
			this.fail(e instanceof BinpazerError ? e : new BinpazerError("io", "sink failed", { cause: e }));
		}
	}

	private check(): void {
		if (this.err !== null) {
			throw this.err;
		}
		if (this.ended) {
			throw new BinpazerError("format", "writer already ended");
		}
	}

	private fail(e: BinpazerError): never {
		this.err = e;
		throw e;
	}
}

function bttEntrySize(nameLen: number): number {
	return align8(20 + 4 + nameLen + 1);
}
