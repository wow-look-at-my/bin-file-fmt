// Codec layer: the registry, the caller-buffer API, and the block-level compress and decompress helpers.

import { deflateRawSync, inflateRawSync, zstdCompressSync, zstdDecompressSync } from "node:zlib";
import { parseEnvelope } from "./envelope.ts";
import type { Compression } from "./envelope.ts";
import { BinpazerError, CODEC_BY_GUID, CODEC_DEFLATE, CODEC_INVALID, CODEC_LZ4, CODEC_STORED, CODEC_ZSTD, FLAG_COMPRESSED, checkU16, formatGuid } from "./format.ts";
import type { Guid } from "./format.ts";
import { lz4FrameBound, lz4FrameCompress, lz4FrameDecompress } from "./lz4.ts";
import type { Block, Reader } from "./reader.ts";
import type { Writer } from "./writer.ts";

export interface Codec {
	// compressInto returns the encoded length and throws "short_buffer" when dst is too small.
	compressInto(src: Uint8Array, dst: Uint8Array): number;
	// decompressInto must produce exactly dst.length bytes.
	decompressInto(src: Uint8Array, dst: Uint8Array): void;
	// bound must never under-report the encoded size of srcLen bytes.
	bound(srcLen: number): number;
}

function shortBuffer(need: number, have: number): BinpazerError {
	return new BinpazerError("short_buffer", `encoded output needs ${need} bytes, the destination holds ${have}`);
}

function wrongLength(got: number, want: number): BinpazerError {
	return new BinpazerError("format", `decoded payload is ${got} bytes, the envelope declares ${want}`);
}

// copyEncoded moves a codec's own output into the caller's destination.
function copyEncoded(out: Uint8Array, dst: Uint8Array): number {
	if (out.length > dst.length) {
		throw shortBuffer(out.length, dst.length);
	}
	dst.set(out);
	return out.length;
}

// copyDecoded checks a decode against the declared length before it lands in dst.
function copyDecoded(out: Uint8Array, dst: Uint8Array): void {
	if (out.length !== dst.length) {
		throw wrongLength(out.length, dst.length);
	}
	dst.set(out);
}

// zlibDecode caps the output at the declared length so a bomb cannot allocate past it.
function zlibDecode(run: (opts: { maxOutputLength: number }) => Uint8Array, dst: Uint8Array): void {
	let out: Uint8Array;
	try {
		out = run({ maxOutputLength: Math.max(1, dst.length) });
	} catch (cause) {
		throw new BinpazerError("format", "codec stream is corrupt or decodes past the declared length", { cause });
	}
	copyDecoded(out, dst);
}

const storedCodec: Codec = {
	compressInto(src, dst) {
		return copyEncoded(src, dst);
	},
	decompressInto(src, dst) {
		copyDecoded(src, dst);
	},
	bound(srcLen) {
		return srcLen;
	},
};

const deflateCodec: Codec = {
	compressInto(src, dst) {
		return copyEncoded(deflateRawSync(src), dst);
	},
	decompressInto(src, dst) {
		zlibDecode((opts) => inflateRawSync(src, opts), dst);
	},
	bound(n) {
		return n + Math.floor(n / 4096) + Math.floor(n / 16384) + Math.floor(n / 33554432) + 13 + 64;
	},
};

const zstdCodec: Codec = {
	compressInto(src, dst) {
		return copyEncoded(zstdCompressSync(src), dst);
	},
	decompressInto(src, dst) {
		zlibDecode((opts) => zstdDecompressSync(src, opts), dst);
	},
	bound(n) {
		const margin = n < 128 * 1024 ? Math.floor((128 * 1024 - n) / 2048) : 0;
		return n + Math.floor(n / 256) + margin + 64;
	},
};

const lz4Codec: Codec = {
	compressInto(src, dst) {
		try {
			return lz4FrameCompress(src, dst);
		} catch (e) {
			if (e instanceof RangeError) {
				throw shortBuffer(lz4FrameBound(src.length), dst.length);
			}
			throw e;
		}
	},
	decompressInto(src, dst) {
		let n: number;
		try {
			n = lz4FrameDecompress(src, dst);
		} catch (cause) {
			throw new BinpazerError("format", "lz4 frame is corrupt or decodes past the declared length", { cause });
		}
		if (n !== dst.length) {
			throw wrongLength(n, dst.length);
		}
	},
	bound(n) {
		return lz4FrameBound(n);
	},
};

const builtins = new Map<number, Codec>([
	[CODEC_STORED, storedCodec],
	[CODEC_DEFLATE, deflateCodec],
	[CODEC_ZSTD, zstdCodec],
	[CODEC_LZ4, lz4Codec],
]);
const registered = new Map<number, Codec>();
const guidRegistered = new Map<string, Codec>();

// registerCodec installs a codec on a registry id; it wins over a built-in on the same id.
export function registerCodec(id: number, codec: Codec): void {
	checkU16(id, "codec id");
	if (id === CODEC_INVALID || id === CODEC_BY_GUID) {
		throw new BinpazerError("range", `codec id ${id} cannot be registered (use registerCodecGuid for 65535)`);
	}
	registered.set(id, codec);
}

export function registerCodecGuid(guid: Guid, codec: Codec): void {
	guidRegistered.set(formatGuid(guid), codec);
}

export function lookupCodec(id: number, guid?: Guid | null): Codec | null {
	if (id === CODEC_BY_GUID) {
		return guid ? (guidRegistered.get(formatGuid(guid)) ?? null) : null;
	}
	return registered.get(id) ?? builtins.get(id) ?? null;
}

function mustLookup(id: number, guid: Guid | null): Codec {
	const c = lookupCodec(id, guid);
	if (c === null) {
		const which = id === CODEC_BY_GUID && guid ? formatGuid(guid) : `id ${id}`;
		throw new BinpazerError("codec_unavailable", `codec ${which} is not available`);
	}
	return c;
}

export function compressBound(id: number, guid: Guid | null, srcLen: number): number {
	return mustLookup(id, guid).bound(srcLen);
}

// compressInto encodes src into dst and returns the encoded length; size dst with compressBound.
export function compressInto(id: number, guid: Guid | null, src: Uint8Array, dst: Uint8Array): number {
	const n = mustLookup(id, guid).compressInto(src, dst);
	if (!Number.isInteger(n) || n < 0 || n > dst.length) {
		throw new BinpazerError("short_buffer", `codec ${id} reported ${n} bytes for a ${dst.length}-byte destination`);
	}
	return n;
}

// decompressInto decodes exactly c.uncompressedLength bytes into the front of dst.
export function decompressInto(c: Compression, stored: Uint8Array, dst: Uint8Array): void {
	const codec = mustLookup(c.codecId, c.codecGuid);
	if (dst.length < c.uncompressedLength) {
		throw new BinpazerError("short_buffer", `destination holds ${dst.length} bytes, the envelope declares ${c.uncompressedLength}`);
	}
	try {
		codec.decompressInto(stored, dst.subarray(0, c.uncompressedLength));
	} catch (cause) {
		if (cause instanceof BinpazerError) {
			throw cause;
		}
		throw new BinpazerError("format", `codec ${c.codecId} failed to decode`, { cause });
	}
}

export function compress(id: number, guid: Guid | null, src: Uint8Array): Uint8Array {
	const dst = new Uint8Array(compressBound(id, guid, src.length));
	return dst.subarray(0, compressInto(id, guid, src, dst));
}

// decompress allocates c.uncompressedLength bytes; gate that untrusted field before calling it.
export function decompress(c: Compression, stored: Uint8Array): Uint8Array {
	const dst = new Uint8Array(c.uncompressedLength);
	decompressInto(c, stored, dst);
	return dst;
}

// compressBlock writes one block from its logical payload, encoded with codecId.
export function compressBlock(w: Writer, typeId: number, flags: number, codecId: number, codecGuid: Guid | null, payload: Uint8Array): void {
	w.checkCanCompress();
	if (codecId === CODEC_BY_GUID && codecGuid === null) {
		throw new BinpazerError("range", "codec id 65535 needs a codec GUID");
	}
	const stored = compress(codecId, codecGuid, payload);
	w.blockCompressed(typeId, flags, codecId, codecGuid, stored, payload.length);
}

// readPayloadDecompressed reads the block and decodes it when it carries an envelope.
export function readPayloadDecompressed(r: Reader, b: Block): Uint8Array {
	const payload = r.readPayload(b);
	if ((b.flags & FLAG_COMPRESSED) === 0) {
		return payload;
	}
	const c = parseEnvelope(payload);
	r.claimDecoded(c.uncompressedLength);
	const dst = new Uint8Array(c.uncompressedLength);
	decompressInto(c, payload.subarray(c.headerSize), dst);
	return dst;
}
