// Shared binpazer constants, GUID helpers, CRC-32C, the error type and the IO interfaces.

export const MAGIC = "binpazer";
export const FOOTER_MAGIC = "rezapnib";
export const VERSION_MAJOR = 1;
export const VERSION_MINOR = 0;
export const VERSION_MINOR_COMPRESSION = 1;
export const VERSION_MINOR_APPENDABLE = 2;
export const HEADER_FLAG_APPENDABLE = 1;
export const LARGE_UNIT = 4096;

export const TYPE_INVALID = 0;
export const TYPE_INVALID_MAX = 65535;
export const TYPE_BLOCK_TYPE_TABLE = 65534;
export const TYPE_BLOCK_INDEX = 65533;
export const TYPE_WRITER_INFO = 65532;
export const TYPE_PADDING = 65531;
export const TYPE_GROUP = 65530;
export const TYPE_END = 65529;

export const FLAG_CRITICAL = 1;
export const FLAG_SAFE_TO_COPY = 2;
export const FLAG_COMPRESSED = 4;
export const FLAG_HAS_CRC = 8;
export const FLAG_LARGE_MODE = 16;

export const CODEC_INVALID = 0;
export const CODEC_STORED = 1;
export const CODEC_DEFLATE = 2;
export const CODEC_ZSTD = 3;
export const CODEC_LZ4 = 4;
export const CODEC_BY_GUID = 65535;

export const ENVELOPE_FIXED_SIZE = 16;
export const ENVELOPE_GUID_SIZE = 32;

// IO_BUF_SIZE is the reader and writer buffer size and the default streaming chunk.
export const IO_BUF_SIZE = 64 * 1024;

// DEFAULT_MAX_ALLOC caps one untrusted length field when the input size is unknown.
export const DEFAULT_MAX_ALLOC = 1024 * 1024 * 1024;

export type ErrorCode =
	| "bad_magic"
	| "version"
	| "format"
	| "range"
	| "crc"
	| "truncated"
	| "capacity"
	| "noseek"
	| "bad_envelope"
	| "short_buffer"
	| "codec_unavailable"
	| "io"
	| "not_appendable"
	| "ended";

export class BinpazerError extends Error {
	readonly code: ErrorCode;

	constructor(code: ErrorCode, message: string, options?: ErrorOptions) {
		super(`binpazer: ${message}`, options);
		this.name = "BinpazerError";
		this.code = code;
	}
}

// Source.read fills dst and returns the byte count; a short count means end of input.
export interface Source {
	read(dst: Uint8Array): number;
	seek?(offset: number): void;
	size?(): number;
}

// Sink.write must consume src before it returns: the writer reuses the buffer.
export interface Sink {
	write(src: Uint8Array): void;
	seek?(offset: number): void;
}

// Guid is several bytes in canonical (RFC 4122 text) order.
export type Guid = Uint8Array;

export interface TypeDef {
	typeId: number;
	guid: Guid;
	name: string;
}

export interface TypeEntry {
	typeId: number;
	guid: Guid;
	name: string;
}

export interface IndexEntry {
	typeId: number;
	offset: number;
}

function wellKnown(last: number): Guid {
	return Uint8Array.of(0x62, 0x69, 0x6e, 0x70, 0x61, 0x7a, 0x65, 0x72, 0, 0, 0, 0, 0, 0, 0, last);
}

export const BLOCK_TYPE_TABLE_GUID: Guid = wellKnown(1);
export const BLOCK_INDEX_GUID: Guid = wellKnown(2);
export const WRITER_INFO_GUID: Guid = wellKnown(3);
export const PADDING_GUID: Guid = wellKnown(4);
export const GROUP_GUID: Guid = wellKnown(5);
export const END_GUID: Guid = wellKnown(6);

const HEX = "0123456789abcdef";

export function formatGuid(g: Guid): string {
	if (g.length !== 16) {
		throw new BinpazerError("range", `a GUID is 16 bytes, got ${g.length}`);
	}
	let s = "";
	for (let i = 0; i < 16; i++) {
		if (i === 4 || i === 6 || i === 8 || i === 10) {
			s += "-";
		}
		s += HEX[g[i] >>> 4] + HEX[g[i] & 15];
	}
	return s;
}

// parseGuid accepts canonical text; hyphens are separators and may appear anywhere.
export function parseGuid(text: string): Guid {
	const clean = text.replaceAll("-", "");
	if (clean.length !== 32 || !/^[0-9a-fA-F]+$/.test(clean)) {
		throw new BinpazerError("format", `invalid GUID ${JSON.stringify(text)} (need 32 hex digits)`);
	}
	const g = new Uint8Array(16);
	for (let i = 0; i < 16; i++) {
		g[i] = parseInt(clean.slice(i * 2, i * 2 + 2), 16);
	}
	return g;
}

export function guidEquals(a: Guid, b: Guid): boolean {
	if (a.length !== b.length) {
		return false;
	}
	for (let i = 0; i < a.length; i++) {
		if (a[i] !== b[i]) {
			return false;
		}
	}
	return true;
}

const CRC_TABLES = ((): Uint32Array => {
	const t = new Uint32Array(8 * 256);
	for (let i = 0; i < 256; i++) {
		let c = i;
		for (let k = 0; k < 8; k++) {
			c = c & 1 ? (c >>> 1) ^ 0x82f63b78 : c >>> 1;
		}
		t[i] = c >>> 0;
	}
	for (let i = 0; i < 256; i++) {
		for (let k = 1; k < 8; k++) {
			const prev = t[(k - 1) * 256 + i];
			t[k * 256 + i] = ((prev >>> 8) ^ t[prev & 255]) >>> 0;
		}
	}
	return t;
})();

export function crc32c(crc: number, data: Uint8Array): number {
	const t = CRC_TABLES;
	let c = ~crc >>> 0;
	let i = 0;
	const n = data.length;
	const end8 = n - (n & 7);
	while (i < end8) {
		const a = (c ^ (data[i] | (data[i + 1] << 8) | (data[i + 2] << 16) | (data[i + 3] << 24))) >>> 0;
		c =
			t[1792 + (a & 255)] ^
			t[1536 + ((a >>> 8) & 255)] ^
			t[1280 + ((a >>> 16) & 255)] ^
			t[1024 + (a >>> 24)] ^
			t[768 + data[i + 4]] ^
			t[512 + data[i + 5]] ^
			t[256 + data[i + 6]] ^
			t[data[i + 7]];
		i += 8;
	}
	while (i < n) {
		c = t[(c ^ data[i]) & 255] ^ (c >>> 8);
		i++;
	}
	return ~c >>> 0;
}

export function align8(x: number): number {
	return Math.ceil(x / 8) * 8;
}

export function putU16(b: Uint8Array, off: number, v: number): void {
	b[off] = v & 255;
	b[off + 1] = (v >>> 8) & 255;
}

export function putU32(b: Uint8Array, off: number, v: number): void {
	b[off] = v & 255;
	b[off + 1] = (v >>> 8) & 255;
	b[off + 2] = (v >>> 16) & 255;
	b[off + 3] = (v >>> 24) & 255;
}

// putU64 writes a safe integer; callers range-check before they call it.
export function putU64(b: Uint8Array, off: number, v: number): void {
	putU32(b, off, v % 0x100000000);
	putU32(b, off + 4, Math.floor(v / 0x100000000));
}

export function getU16(b: Uint8Array, off: number): number {
	return b[off] | (b[off + 1] << 8);
}

export function getU32(b: Uint8Array, off: number): number {
	return (b[off] | (b[off + 1] << 8) | (b[off + 2] << 16) | (b[off + 3] << 24)) >>> 0;
}

// getU64 returns null for the all-ones sentinel and throws "range" above MAX_SAFE_INTEGER.
export function getU64(b: Uint8Array, off: number, what: string): number | null {
	const lo = getU32(b, off);
	const hi = getU32(b, off + 4);
	if (lo === 0xffffffff && hi === 0xffffffff) {
		return null;
	}
	if (hi > 0x1fffff) {
		throw new BinpazerError("range", `${what} does not fit a safe integer`);
	}
	return hi * 0x100000000 + lo;
}

// decodePayloadLength converts the on-disk length field to a byte count.
export function decodePayloadLength(lengthField: number, flags: number): number {
	return flags & FLAG_LARGE_MODE ? lengthField * LARGE_UNIT : lengthField;
}

// payloadClaim is the payload plus its CRC trailer: the bytes a block header promises.
export function payloadClaim(payloadLength: number, flags: number): number {
	return flags & FLAG_HAS_CRC ? payloadLength + 4 : payloadLength;
}

// blockHeaderBytes rebuilds a block's 8-byte header, the first bytes its CRC covers.
export function blockHeaderBytes(typeId: number, flags: number, payloadLength: number): Uint8Array {
	const bh = new Uint8Array(8);
	putU16(bh, 0, typeId);
	putU16(bh, 2, flags);
	putU32(bh, 4, flags & FLAG_LARGE_MODE ? Math.floor(payloadLength / LARGE_UNIT) : payloadLength);
	return bh;
}

export function checkU16(v: number, what: string): void {
	if (!Number.isInteger(v) || v < 0 || v > 0xffff) {
		throw new BinpazerError("range", `${what} ${v} is not a u16`);
	}
}

export function checkGuid(g: Guid, what: string): void {
	if (g.length !== 16) {
		throw new BinpazerError("range", `${what} must be 16 bytes, got ${g.length}`);
	}
}
