// Codec layer: every registry codec round-trips, bounds hold, and bad lengths and unknown codecs fail typed.

import { compress, compressBlock, compressBound, compressInto, decompress, decompressInto, lookupCodec, readPayloadDecompressed, registerCodec, registerCodecGuid } from "./codec.ts";
import type { Codec } from "./codec.ts";
import type { Compression } from "./envelope.ts";
import { CODEC_BY_GUID, CODEC_DEFLATE, CODEC_LZ4, CODEC_STORED, CODEC_ZSTD, FLAG_COMPRESSED, FLAG_HAS_CRC } from "./format.ts";
import type { Guid } from "./format.ts";
import { MemorySink as Mem, MemorySource as Src } from "./memory.ts";
import { Reader as Rd } from "./reader.ts";
import { SAMPLE_NOTE, SAMPLE_WRITER, assert, codeOf, enc, guidOf, mustNext, pattern, test } from "./testio.ts";
import { Writer } from "./writer.ts";

const CODECS = [
	{ id: CODEC_STORED, name: "stored" },
	{ id: CODEC_DEFLATE, name: "deflate" },
	{ id: CODEC_ZSTD, name: "zstd" },
	{ id: CODEC_LZ4, name: "lz4" },
];

function compressible(n: number): Uint8Array {
	const line = enc("binpazer compression parity across implementations. ");
	const out = new Uint8Array(n);
	for (let i = 0; i < n; i++) {
		out[i] = line[i % line.length];
	}
	return out;
}

function envelope(codecId: number, codecGuid: Guid | null, uncompressedLength: number, storedLength: number): Compression {
	return { codecId, codecGuid, uncompressedLength, storedLength, headerSize: codecGuid === null ? 16 : 32 };
}

// writeOne writes a file holding one block per payload, each through compressBlock.
function writeOne(codecId: number, codecGuid: Guid | null, payloads: Uint8Array[]): Uint8Array {
	const sink = new Mem();
	const w = new Writer(sink, SAMPLE_WRITER, "codec", [{ typeId: 1, guid: SAMPLE_NOTE, name: "Note" }]);
	for (const p of payloads) {
		compressBlock(w, 1, FLAG_HAS_CRC, codecId, codecGuid, p);
	}
	w.end();
	return sink.bytes();
}

test("each codec round-trips empty, small, compressible and random payloads through a file", () => {
	const inputs = [new Uint8Array(0), enc("hi"), compressible(100_000), pattern(70_000, 9)];
	for (const c of CODECS) {
		const bytes = writeOne(c.id, null, inputs);
		const cr = new Rd(new Src(bytes));
		assert.equal(cr.versionMinor, 1, c.name);
		for (const want of inputs) {
			const b = mustNext(cr);
			assert.equal(b.flags, FLAG_HAS_CRC | FLAG_COMPRESSED, c.name);
			assert.deepEqual(readPayloadDecompressed(cr, b), want, `${c.name} at ${want.length} bytes`);
		}
		assert.equal(cr.next(), null);
	}
});

test("real codecs shrink compressible input", () => {
	const src = compressible(8192);
	for (const c of CODECS.slice(1)) {
		const stored = compress(c.id, null, src);
		assert.ok(stored.length < src.length / 4, `${c.name}: ${stored.length}`);
		assert.deepEqual(decompress(envelope(c.id, null, src.length, stored.length), stored), src);
	}
});

test("compressBound covers incompressible input for every codec", () => {
	for (const c of CODECS) {
		for (const n of [0, 1, 15, 1024, 64 * 1024, 300 * 1024]) {
			const src = pattern(n, n + 3);
			const dst = new Uint8Array(compressBound(c.id, null, n));
			const got = compressInto(c.id, null, src, dst);
			assert.ok(got <= dst.length, `${c.name} at ${n}`);
			const back = new Uint8Array(n);
			decompressInto(envelope(c.id, null, n, got), dst.subarray(0, got), back);
			assert.deepEqual(back, src, `${c.name} at ${n}`);
		}
	}
});

test("a destination too small for the encoded bytes gives short_buffer", () => {
	const src = compressible(10_000);
	for (const c of CODECS) {
		const need = compress(c.id, null, src).length;
		assert.equal(codeOf(() => compressInto(c.id, null, src, new Uint8Array(need - 1))), "short_buffer", c.name);
		assert.equal(codeOf(() => decompressInto(envelope(c.id, null, src.length, need), new Uint8Array(need), new Uint8Array(src.length - 1))), "short_buffer", c.name);
	}
});

test("a decode of any other length than uncompressed_length gives format", () => {
	const src = compressible(5000);
	for (const c of CODECS) {
		const stored = compress(c.id, null, src);
		for (const lie of [src.length - 1, src.length + 1, 0]) {
			const code = codeOf(() => decompressInto(envelope(c.id, null, lie, stored.length), stored, new Uint8Array(lie)));
			assert.equal(code, "format", `${c.name} declared ${lie}`);
		}
	}
});

test("a lying envelope in a file gives format, and a corrupt stream gives format", () => {
	for (const c of CODECS) {
		const sink = new Mem();
		const w = new Writer(sink, SAMPLE_WRITER, "lie", []);
		const src = compressible(3000);
		w.blockCompressed(1, 0, c.id, null, compress(c.id, null, src), src.length + 10);
		w.blockCompressed(1, 0, c.id, null, pattern(64, 5), 3000);
		w.end();
		const cr = new Rd(new Src(sink.bytes()));
		assert.equal(codeOf(() => readPayloadDecompressed(cr, mustNext(cr))), "format", `${c.name} lie`);
		if (c.id !== CODEC_STORED) {
			assert.equal(codeOf(() => readPayloadDecompressed(cr, mustNext(cr))), "format", `${c.name} garbage`);
		}
	}
});

test("a decompression bomb stops at the declared length", () => {
	const zeros = new Uint8Array(4 * 1024 * 1024);
	for (const c of CODECS.slice(1)) {
		const stored = compress(c.id, null, zeros);
		assert.equal(codeOf(() => decompressInto(envelope(c.id, null, 1000, stored.length), stored, new Uint8Array(1000))), "format", c.name);
	}
});

test("uncompressed_length passes the reader's allocation gate before decoding", () => {
	const sink = new Mem();
	const w = new Writer(sink, SAMPLE_WRITER, "gate", []);
	w.blockCompressed(1, 0, CODEC_STORED, null, enc("tiny"), 2 ** 40);
	w.blockCompressed(1, 0, CODEC_STORED, null, enc("tiny"), 5000);
	w.end();
	const cr = new Rd(new Src(sink.bytes()), { maxAlloc: 4096 });
	assert.equal(codeOf(() => readPayloadDecompressed(cr, mustNext(cr))), "capacity");
	const again = new Rd(new Src(sink.bytes()), { maxAlloc: 4096 });
	again.skipPayload(mustNext(again));
	assert.equal(codeOf(() => readPayloadDecompressed(again, mustNext(again))), "capacity");
});

test("an uncompressed block comes back as its payload", () => {
	const sink = new Mem();
	const w = new Writer(sink, SAMPLE_WRITER, "plain", []);
	w.block(1, FLAG_HAS_CRC, enc("as is"));
	w.end();
	const cr = new Rd(new Src(sink.bytes()));
	assert.deepEqual(readPayloadDecompressed(cr, mustNext(cr)), enc("as is"));
});

const xorCodec: Codec = {
	compressInto(src, dst) {
		if (dst.length < src.length + 1) {
			throw new RangeError("small");
		}
		dst[0] = 0x5a;
		src.forEach((v, i) => (dst[i + 1] = v ^ 0x5a));
		return src.length + 1;
	},
	decompressInto(src, dst) {
		if (src.length !== dst.length + 1 || src[0] !== 0x5a) {
			throw new Error("bad xor stream");
		}
		for (let i = 0; i < dst.length; i++) {
			dst[i] = src[i + 1] ^ 0x5a;
		}
	},
	bound(n) {
		return n + 1;
	},
};

test("a GUID-registered codec round-trips through a file", () => {
	const g = guidOf(0x77, 0x01, 0x02);
	registerCodecGuid(g, xorCodec);
	assert.equal(lookupCodec(CODEC_BY_GUID, g), xorCodec);
	const data = pattern(999, 4);
	const bytes = writeOne(CODEC_BY_GUID, g, [data]);
	const cr = new Rd(new Src(bytes));
	const b = mustNext(cr);
	assert.deepEqual(readPayloadDecompressed(cr, b), data);
	assert.equal(codeOf(() => compressBlock(new Writer(new Mem(), SAMPLE_WRITER, "x", []), 1, 0, CODEC_BY_GUID, null, data)), "range");
});

test("registerCodec overrides a built-in on its id and refuses ids 0 and 65535", () => {
	assert.equal(codeOf(() => registerCodec(0, xorCodec)), "range");
	assert.equal(codeOf(() => registerCodec(CODEC_BY_GUID, xorCodec)), "range");
	registerCodec(900, xorCodec);
	assert.equal(lookupCodec(900), xorCodec);
	const data = enc("override");
	const cr = new Rd(new Src(writeOne(900, null, [data])));
	assert.deepEqual(readPayloadDecompressed(cr, mustNext(cr)), data);
	const original = lookupCodec(CODEC_STORED);
	assert.ok(original !== null);
	registerCodec(CODEC_STORED, xorCodec);
	try {
		assert.equal(lookupCodec(CODEC_STORED), xorCodec);
	} finally {
		registerCodec(CODEC_STORED, original);
	}
});

test("an unknown codec gives codec_unavailable", () => {
	assert.equal(lookupCodec(4242), null);
	assert.equal(lookupCodec(CODEC_BY_GUID, guidOf(0xee)), null);
	assert.equal(codeOf(() => compressBound(4242, null, 10)), "codec_unavailable");
	assert.equal(codeOf(() => compress(CODEC_BY_GUID, guidOf(0xee), enc("x"))), "codec_unavailable");
	const sink = new Mem();
	const w = new Writer(sink, SAMPLE_WRITER, "unknown", []);
	w.blockCompressed(1, 0, 4242, null, enc("opaque"), 6);
	w.blockCompressed(1, 0, CODEC_BY_GUID, guidOf(0xee), enc("opaque"), 6);
	w.end();
	const cr = new Rd(new Src(sink.bytes()));
	assert.equal(codeOf(() => readPayloadDecompressed(cr, mustNext(cr))), "codec_unavailable");
	assert.equal(codeOf(() => readPayloadDecompressed(cr, mustNext(cr))), "codec_unavailable");
});
