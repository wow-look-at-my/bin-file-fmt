// Reader parses a binpazer file from a Source and gates every untrusted length before it allocates.

import { BinpazerError, DEFAULT_MAX_ALLOC, FLAG_HAS_CRC, FOOTER_MAGIC, HEADER_FLAG_APPENDABLE, IO_BUF_SIZE, MAGIC, TYPE_BLOCK_TYPE_TABLE, TYPE_END, VERSION_MAJOR } from "./format.ts";
import { align8, blockHeaderBytes, crc32c, decodePayloadLength, getU16, getU32, getU64, payloadClaim } from "./format.ts";
import type { Guid, Source, TypeEntry } from "./format.ts";

const MIN_BTT_ENTRY_SIZE = 32;
// The most the first buffer fill after a seek asks the source for.
const SEEK_READ_AHEAD = 4096;
const decoder = new TextDecoder();

export interface ReaderOptions {
	maxAlloc?: number;
}

export interface Block {
	typeId: number;
	flags: number;
	payloadLength: number;
	payloadOffset: number;
	offset: number;
	guid: Guid | null;
	known: boolean;
	name: string;
}

function matchesAscii(b: Uint8Array, at: number, text: string): boolean {
	for (let i = 0; i < text.length; i++) {
		if (b[at + i] !== text.charCodeAt(i)) {
			return false;
		}
	}
	return true;
}

// isStreamFooter reports whether the bytes at b[at] are the footer of a file
// with no file_length; pos is their file offset.
function isStreamFooter(b: Uint8Array, at: number, pos: number): boolean {
	if (!matchesAscii(b, at + 8, FOOTER_MAGIC)) {
		return false;
	}
	const lo = getU32(b, at);
	const idx = getU32(b, at + 4) * 0x100000000 + lo;
	return lo % 8 === 0 && idx < pos;
}

// looseU64 reads file_length without the safe-integer check: it is only
// compared, never used as a position.
function looseU64(b: Uint8Array, at: number): number | null {
	const lo = getU32(b, at);
	const hi = getU32(b, at + 4);
	if (lo === 0xffffffff && hi === 0xffffffff) {
		return null;
	}
	return hi * 0x100000000 + lo;
}

function nextOffset(b: Block): number {
	return align8(b.payloadOffset + payloadClaim(b.payloadLength, b.flags));
}

export class Reader {
	readonly versionMajor: number;
	readonly versionMinor: number;
	readonly writerGuid: Guid;
	readonly writerName: string;
	// fileLength is null for the streaming sentinel and inexact above MAX_SAFE_INTEGER.
	readonly fileLength: number | null;
	readonly firstBlockOffset: number;
	readonly types: TypeEntry[];
	readonly headerFlags: number;

	private readonly src: Source;
	private readonly win = new Uint8Array(IO_BUF_SIZE);
	private winPos = 0;
	private winEnd = 0;
	private srcEof = false;
	private shortFill = false;
	private cursor = 0;
	private size: number;
	private tableEnd = 0;
	private sawEnd = false;
	private lastTorn = false;
	private readonly maxAlloc: number;
	private readonly typeIdx = new Map<number, number>();
	private indexFound = false;
	private indexOff = 0;
	private indexUnsafe = false;
	private blocksEnd: number | null;

	constructor(source: Source, opts?: ReaderOptions) {
		this.src = source;
		const cap = opts?.maxAlloc ?? 0;
		this.maxAlloc = cap > 0 ? cap : DEFAULT_MAX_ALLOC;
		const sizeFn = source.size;
		this.size = sizeFn !== undefined ? this.io(() => sizeFn.call(source)) : -1;

		const fixed = new Uint8Array(56);
		this.readExact(fixed);
		if (!matchesAscii(fixed, 0, MAGIC)) {
			throw new BinpazerError("bad_magic", "bad magic");
		}
		if (fixed[8] !== 0x0d || fixed[9] !== 0x0a || fixed[10] !== 0x1a || fixed[11] !== 0x0a) {
			throw new BinpazerError("bad_magic", "bad signature");
		}
		this.versionMajor = getU16(fixed, 12);
		this.versionMinor = getU16(fixed, 14);
		if (this.versionMajor > VERSION_MAJOR) {
			throw new BinpazerError("version", `unsupported major version ${this.versionMajor}`);
		}
		this.writerGuid = fixed.slice(16, 32);
		this.fileLength = looseU64(fixed, 32);
		const fbo = getU64(fixed, 40, "first_block_offset");
		if (fbo === null) {
			throw new BinpazerError("range", "first_block_offset does not fit a safe integer");
		}
		this.firstBlockOffset = fbo;
		this.headerFlags = getU32(fixed, 48);

		const lenBuf = new Uint8Array(4);
		this.readExact(lenBuf);
		const nameLen = getU32(lenBuf, 0);
		this.claim(nameLen, "writer name");
		const nameBytes = new Uint8Array(nameLen);
		this.readExact(nameBytes);
		this.writerName = decoder.decode(nameBytes);
		this.skip(1);
		if (this.cursor > this.firstBlockOffset) {
			throw new BinpazerError("format", "header overruns first_block_offset");
		}
		this.skip(this.firstBlockOffset - this.cursor);

		this.types = this.readBTT();
		this.tableEnd = this.cursor;

		const fl = this.fileLength;
		if (source.seek !== undefined && fl !== null && fl >= 16 && this.size >= 0 && fl <= this.size) {
			const saved = this.cursor;
			this.seekTo(fl - 16);
			const foot = new Uint8Array(16);
			if (this.readUpTo(foot) === 16 && matchesAscii(foot, 8, FOOTER_MAGIC)) {
				this.recordIndex(foot, 0);
			}
			this.seekTo(saved);
		}
		this.blocksEnd = fl;
		if (fl !== null && this.indexFound) {
			this.blocksEnd = fl - 16;
		}
		// A streamed file has no file_length, but the real input size shows where a footer must be.
		if (source.seek !== undefined && fl === null && !this.appendable && this.size >= 16) {
			const saved = this.cursor;
			const at = this.size - 16;
			this.seekTo(at);
			const foot = new Uint8Array(16);
			if (this.readUpTo(foot) === 16 && isStreamFooter(foot, 0, at)) {
				this.recordIndex(foot, 0);
				this.blocksEnd = at;
			}
			this.seekTo(saved);
		}
	}

	// hasIndex reports whether a Block Index was located through the footer.
	get hasIndex(): boolean {
		return this.indexFound;
	}

	get indexOffset(): number {
		return this.indexOff;
	}

	// offset is the logical position of the next byte the reader consumes.
	get offset(): number {
		return this.cursor;
	}

	get inputSize(): number {
		return this.size;
	}

	// appendable reports the appendable header flag.
	get appendable(): boolean {
		return (this.headerFlags & HEADER_FLAG_APPENDABLE) !== 0;
	}

	// ended reports that the walk reached the End block of an appendable file.
	get ended(): boolean {
		return this.sawEnd;
	}

	// torn reports that the last "pending" from nextCommitted was a whole block whose seal is absent or wrong.
	get torn(): boolean {
		return this.lastTorn;
	}

	// next reads the next block header and returns null after the last block.
	next(): Block | null {
		if (this.sawEnd || (this.blocksEnd !== null && this.cursor >= this.blocksEnd)) {
			return null;
		}
		const fl = this.fileLength;
		if (!this.indexFound && fl !== null && fl >= 16 && this.cursor === fl - 16 && this.ensure(16) >= 16) {
			if (matchesAscii(this.win, this.winPos + 8, FOOTER_MAGIC)) {
				this.recordIndex(this.win, this.winPos);
				this.winPos += 16;
				this.cursor += 16;
				return null;
			}
		}
		// With no file_length the footer is the last several bytes, so the check needs one byte past them.
		if (!this.indexFound && fl === null && !this.appendable && this.ensureAtLeast(17) === 16 &&
			isStreamFooter(this.win, this.winPos, this.cursor)) {
			this.recordIndex(this.win, this.winPos);
			this.winPos += 16;
			this.cursor += 16;
			this.blocksEnd = this.cursor - 16;
			return null;
		}
		const hdr8 = new Uint8Array(8);
		const got = this.readUpTo(hdr8);
		if (got === 0) {
			return null;
		}
		if (got < 8) {
			throw this.truncated();
		}
		return this.noteEnd(this.parseHeader(hdr8, this.cursor - 8));
	}

	// nextCommitted returns the next committed block, "pending" with the reader unmoved, or null after the End block.
	// It needs seek and size, like Go and C, because it must learn how much of a growing file is there.
	nextCommitted(): Block | "pending" | null {
		const seekFn = this.src.seek;
		const sizeFn = this.src.size;
		if (seekFn === undefined || sizeFn === undefined) {
			throw new BinpazerError("noseek", "nextCommitted needs a source that can seek and report its size");
		}
		if (!this.appendable) {
			throw new BinpazerError("not_appendable", "the file does not set the appendable header flag");
		}
		if (this.sawEnd || (this.blocksEnd !== null && this.cursor >= this.blocksEnd)) {
			return null;
		}
		const start = this.cursor;
		this.size = this.io(() => sizeFn.call(this.src));
		this.seekTo(start);
		const h = this.verifyCommitted(start);
		if (h === null) {
			this.seekTo(start);
			return "pending";
		}
		this.seekTo(start + 8);
		return this.noteEnd(this.parseHeader(h, start));
	}

	// verifyCommitted checks the seal of the block at start and returns its header, or null when it is not committed.
	private verifyCommitted(start: number): Uint8Array | null {
		this.lastTorn = false;
		if (this.size < start + 8) {
			return null;
		}
		const h = new Uint8Array(8);
		this.readExact(h);
		const typeId = getU16(h, 0);
		const flags = getU16(h, 2);
		const payloadLength = decodePayloadLength(getU32(h, 4), flags);
		if (typeId === 0) {
			return null;
		}
		if ((flags & FLAG_HAS_CRC) === 0) {
			this.lastTorn = true;
			return null;
		}
		if (align8(start + 8 + payloadLength + 4) > this.size) {
			return null;
		}
		let crc = crc32c(0, h);
		const chunkBuf = new Uint8Array(Math.min(payloadLength, IO_BUF_SIZE));
		for (let rem = payloadLength; rem > 0; ) {
			const chunk = chunkBuf.subarray(0, Math.min(rem, chunkBuf.length));
			this.readExact(chunk);
			crc = crc32c(crc, chunk);
			rem -= chunk.length;
		}
		const seal = new Uint8Array(4);
		this.readExact(seal);
		if (crc !== getU32(seal, 0)) {
			this.lastTorn = true;
			return null;
		}
		return h;
	}

	private noteEnd(b: Block): Block {
		if (b.typeId === TYPE_END && this.appendable) {
			this.sawEnd = true;
		}
		return b;
	}

	readPayload(b: Block): Uint8Array {
		this.claim(payloadClaim(b.payloadLength, b.flags), "block payload");
		const dst = new Uint8Array(b.payloadLength);
		this.readPayloadInto(b, dst);
		return dst;
	}

	// readPayloadInto fills dst with the payload, verifies the CRC, and returns the byte count.
	readPayloadInto(b: Block, dst: Uint8Array): number {
		if (dst.length < b.payloadLength) {
			throw new BinpazerError("short_buffer", `destination holds ${dst.length} bytes, payload is ${b.payloadLength}`);
		}
		this.claim(payloadClaim(b.payloadLength, b.flags), "block payload");
		const view = dst.subarray(0, b.payloadLength);
		this.readExact(view);
		if (b.flags & FLAG_HAS_CRC) {
			this.verifyCRC(b, crc32c(crc32c(0, blockHeaderBytes(b.typeId, b.flags, b.payloadLength)), view));
		}
		this.advancePast(b);
		return b.payloadLength;
	}

	// streamPayload hands the payload to sink in chunks; a CRC failure arrives after the last chunk.
	streamPayload(b: Block, sink: (chunk: Uint8Array) => void, scratch?: Uint8Array): void {
		this.claim(payloadClaim(b.payloadLength, b.flags), "block payload");
		const chunkBuf = scratch ?? new Uint8Array(Math.min(b.payloadLength, IO_BUF_SIZE));
		if (chunkBuf.length === 0 && b.payloadLength > 0) {
			throw new BinpazerError("range", "streamPayload needs a non-empty scratch buffer");
		}
		const hasCRC = (b.flags & FLAG_HAS_CRC) !== 0;
		let crc = hasCRC ? crc32c(0, blockHeaderBytes(b.typeId, b.flags, b.payloadLength)) : 0;
		for (let rem = b.payloadLength; rem > 0; ) {
			const chunk = chunkBuf.subarray(0, Math.min(rem, chunkBuf.length));
			this.readExact(chunk);
			if (hasCRC) {
				crc = crc32c(crc, chunk);
			}
			sink(chunk);
			rem -= chunk.length;
		}
		if (hasCRC) {
			this.verifyCRC(b, crc);
		}
		this.advancePast(b);
	}

	skipPayload(b: Block): void {
		this.advancePast(b);
	}

	// find returns the offsets of blocks of typeId, through the Block Index when present; it keeps the position.
	find(typeId: number): number[] {
		this.needSeek("find");
		const saved = this.cursor;
		try {
			if (this.indexFound) {
				return this.findIndexed(typeId);
			}
			return this.appendable ? this.findCommitted(typeId) : this.findWalk(typeId);
		} finally {
			this.seekTo(saved);
		}
	}

	// at reads the header of the block at offset; readPayload then reads that block.
	at(offset: number): Block {
		this.needSeek("at");
		if (!Number.isSafeInteger(offset) || offset < 0) {
			throw new BinpazerError("range", `block offset ${offset} is invalid`);
		}
		if (this.size >= 0 && offset > this.size) {
			throw new BinpazerError("truncated", `block offset ${offset} is beyond the ${this.size}-byte input`);
		}
		this.seekTo(offset);
		const hdr8 = new Uint8Array(8);
		this.readExact(hdr8);
		return this.parseHeader(hdr8, offset);
	}

	// claimDecoded is the allocation gate for decompressed bytes.
	claimDecoded(n: number): void {
		if (n > this.maxAlloc) {
			throw new BinpazerError("capacity", `decompressed payload claims ${n} bytes, over the ${this.maxAlloc}-byte cap`);
		}
	}

	// recordIndex notes the footer like Go does; an unsafe offset fails only when find uses it.
	private recordIndex(foot: Uint8Array, at: number): void {
		this.indexFound = true;
		this.indexUnsafe = getU32(foot, at + 4) > 0x1fffff;
		this.indexOff = this.indexUnsafe ? -1 : (getU64(foot, at, "footer index_block_offset") ?? -1);
	}

	private findIndexed(typeId: number): number[] {
		if (this.indexUnsafe) {
			throw new BinpazerError("range", "footer index_block_offset does not fit a safe integer");
		}
		this.seekTo(this.indexOff);
		const head = new Uint8Array(16);
		this.readExact(head);
		const count = getU32(head, 8);
		this.claim(count * 16, "block index");
		const out: number[] = [];
		const entry = new Uint8Array(16);
		for (let i = 0; i < count; i++) {
			this.readExact(entry);
			if (getU16(entry, 0) === typeId) {
				const o = getU64(entry, 8, "index entry offset");
				if (o === null) {
					throw new BinpazerError("range", "index entry offset does not fit a safe integer");
				}
				out.push(o);
			}
		}
		return out;
	}

	// findCommitted walks an appendable file's committed blocks; the unsealed table counts by position.
	private findCommitted(typeId: number): number[] {
		const sawEnd = this.sawEnd;
		this.sawEnd = false;
		try {
			const out: number[] = typeId === TYPE_BLOCK_TYPE_TABLE ? [this.firstBlockOffset] : [];
			this.seekTo(this.tableEnd);
			for (;;) {
				const b = this.nextCommitted();
				if (b === null || b === "pending") {
					return out;
				}
				if (b.typeId === typeId) {
					out.push(b.offset);
				}
				this.skipPayload(b);
			}
		} finally {
			this.sawEnd = sawEnd;
		}
	}

	private findWalk(typeId: number): number[] {
		const out: number[] = [];
		this.seekTo(this.firstBlockOffset);
		const hdr8 = new Uint8Array(8);
		while (this.blocksEnd === null || this.cursor < this.blocksEnd) {
			const at = this.cursor;
			const got = this.readUpTo(hdr8);
			if (got === 0) {
				break;
			}
			if (got < 8) {
				throw this.truncated();
			}
			const flags = getU16(hdr8, 2);
			const plen = decodePayloadLength(getU32(hdr8, 4), flags);
			this.claim(payloadClaim(plen, flags), "block payload");
			if (getU16(hdr8, 0) === typeId) {
				out.push(at);
			}
			this.seekTo(align8(at + 8 + payloadClaim(plen, flags)));
		}
		return out;
	}

	private parseHeader(h: Uint8Array, at: number): Block {
		const typeId = getU16(h, 0);
		const flags = getU16(h, 2);
		const payloadLength = decodePayloadLength(getU32(h, 4), flags);
		this.claim(payloadClaim(payloadLength, flags), "block payload");
		const i = this.typeIdx.get(typeId);
		const t = i === undefined ? null : this.types[i];
		return {
			typeId,
			flags,
			payloadLength,
			payloadOffset: at + 8,
			offset: at,
			guid: t === null ? null : t.guid,
			known: t !== null,
			name: t === null ? "" : t.name,
		};
	}

	private readBTT(): TypeEntry[] {
		const head = new Uint8Array(16);
		this.readExact(head.subarray(0, 8));
		if (getU16(head, 0) !== TYPE_BLOCK_TYPE_TABLE) {
			throw new BinpazerError("format", "first block is not the block type table");
		}
		const bttLen = getU32(head, 4);
		this.claim(bttLen, "block type table");
		const bttEnd = align8(this.firstBlockOffset + 8 + bttLen);
		this.readExact(head.subarray(8, 16));
		const count = getU32(head, 8);
		const maxEntries = bttLen >= 8 ? Math.floor((bttLen - 8) / MIN_BTT_ENTRY_SIZE) : 0;
		if (count > maxEntries) {
			throw new BinpazerError("capacity", `block type table claims ${count} entries but its ${bttLen}-byte payload holds at most ${maxEntries}`);
		}
		const types: TypeEntry[] = [];
		const eh = new Uint8Array(24);
		for (let i = 0; i < count; i++) {
			this.readExact(eh);
			const typeId = getU16(eh, 0);
			const guid = eh.slice(4, 20);
			const nlen = getU32(eh, 20);
			let name = "";
			if (nlen > 0) {
				if (this.cursor >= bttEnd || nlen > bttEnd - this.cursor) {
					throw new BinpazerError("capacity", `block type table entry ${i} name (${nlen} bytes) overruns the table`);
				}
				const nb = new Uint8Array(nlen);
				this.readExact(nb);
				name = decoder.decode(nb);
			}
			this.skip(1);
			const entryRaw = 25 + nlen;
			this.skip(align8(entryRaw) - entryRaw);
			if (!this.typeIdx.has(typeId)) {
				this.typeIdx.set(typeId, types.length);
			}
			types.push({ typeId, guid, name });
		}
		if (this.cursor > bttEnd) {
			throw new BinpazerError("format", "block type table overrun");
		}
		this.skip(bttEnd - this.cursor);
		return types;
	}

	private verifyCRC(b: Block, crc: number): void {
		const trailer = new Uint8Array(4);
		this.readExact(trailer);
		if (crc !== getU32(trailer, 0)) {
			throw new BinpazerError("crc", `CRC mismatch in block at offset ${b.offset}`);
		}
	}

	private advancePast(b: Block): void {
		const next = nextOffset(b);
		if (next < this.cursor) {
			throw new BinpazerError("format", `reader at offset ${this.cursor} is past the block that ends at ${next}`);
		}
		this.skip(next - this.cursor);
	}

	// claim checks that a length from the file can be satisfied before anything is sized from it.
	private claim(n: number, what: string): void {
		if (this.size >= 0) {
			const rem = Math.max(0, this.size - this.cursor);
			if (n > rem) {
				throw new BinpazerError("truncated", `${what} claims ${n} bytes but only ${rem} remain in the input`);
			}
			return;
		}
		if (n > this.maxAlloc) {
			throw new BinpazerError("capacity", `${what} claims ${n} bytes, over the ${this.maxAlloc}-byte cap for an input of unknown size`);
		}
	}

	private needSeek(op: string): void {
		if (this.src.seek === undefined) {
			throw new BinpazerError("noseek", `${op} needs a seekable source`);
		}
	}

	private truncated(): BinpazerError {
		return new BinpazerError("truncated", `unexpected end of input at offset ${this.cursor}`);
	}

	// seekTo is the place that repositions the source; it drops the buffer.
	private seekTo(abs: number): void {
		const seekFn = this.src.seek;
		if (seekFn === undefined) {
			throw new BinpazerError("noseek", "seek on a non-seekable source");
		}
		this.io(() => seekFn.call(this.src, abs));
		this.winPos = 0;
		this.winEnd = 0;
		this.srcEof = false;
		this.shortFill = true;
		this.cursor = abs;
	}

	private skip(n: number): void {
		if (n <= 0) {
			return;
		}
		if (n <= this.winEnd - this.winPos) {
			this.winPos += n;
			this.cursor += n;
			return;
		}
		if (this.src.seek !== undefined && this.size >= 0) {
			const target = this.cursor + n;
			if (target > this.size) {
				this.seekTo(this.size);
				throw this.truncated();
			}
			this.seekTo(target);
			return;
		}
		let rem = n;
		while (rem > 0) {
			if (this.winEnd === this.winPos && this.fill() === 0) {
				throw this.truncated();
			}
			const c = Math.min(rem, this.winEnd - this.winPos);
			this.winPos += c;
			this.cursor += c;
			rem -= c;
		}
	}

	private readExact(dst: Uint8Array): void {
		if (this.readUpTo(dst) < dst.length) {
			throw this.truncated();
		}
	}

	// readUpTo fills dst and returns a short count only at end of input; large reads bypass the buffer.
	private readUpTo(dst: Uint8Array): number {
		let got = Math.min(dst.length, this.winEnd - this.winPos);
		dst.set(this.win.subarray(this.winPos, this.winPos + got));
		this.winPos += got;
		this.cursor += got;
		while (got < dst.length && !this.srcEof) {
			const want = dst.length - got;
			if (want >= this.win.length) {
				const n = this.rawRead(dst.subarray(got));
				got += n;
				this.cursor += n;
				continue;
			}
			if (this.fill() === 0) {
				break;
			}
			const c = Math.min(want, this.winEnd - this.winPos);
			dst.set(this.win.subarray(this.winPos, this.winPos + c), got);
			this.winPos += c;
			this.cursor += c;
			got += c;
		}
		return got;
	}

	// ensure buffers up to n bytes without consuming them and returns how many are available.
	private ensure(n: number): number {
		if (this.winEnd - this.winPos < n && !this.srcEof) {
			this.fill();
		}
		return this.winEnd - this.winPos;
	}

	// ensureAtLeast buffers n bytes, or every byte left before the end of the input, and returns how many are available.
	private ensureAtLeast(n: number): number {
		while (this.winEnd - this.winPos < n && this.fill() > 0) {
			// fill until n bytes or the end of the input
		}
		return this.winEnd - this.winPos;
	}

	// fill compacts the buffer, reads more into it, and returns the bytes added.
	private fill(): number {
		if (this.srcEof) {
			return 0;
		}
		const keep = this.winEnd - this.winPos;
		this.win.copyWithin(0, this.winPos, this.winEnd);
		this.winPos = 0;
		this.winEnd = keep;
		// A full refill after a seek would cost a whole buffer to hand over one small block.
		const end = this.shortFill ? Math.min(this.win.length, keep + SEEK_READ_AHEAD) : this.win.length;
		this.shortFill = false;
		const n = this.rawRead(this.win.subarray(keep, end));
		this.winEnd += n;
		return n;
	}

	private rawRead(dst: Uint8Array): number {
		const n = this.io(() => this.src.read(dst));
		if (!Number.isInteger(n) || n < 0 || n > dst.length) {
			throw new BinpazerError("io", `source returned an invalid byte count ${n}`);
		}
		if (n < dst.length) {
			this.srcEof = true;
		}
		return n;
	}

	private io<T>(f: () => T): T {
		try {
			return f();
		} catch (cause) {
			if (cause instanceof BinpazerError) {
				throw cause;
			}
			throw new BinpazerError("io", "source failed", { cause });
		}
	}
}
