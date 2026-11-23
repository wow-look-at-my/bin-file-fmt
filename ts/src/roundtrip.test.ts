// Writer and reader round trips: flags, large_mode, index, find, streaming, and the seek rules.

import { compressBlock, readPayloadDecompressed } from "./codec.ts";
import { CODEC_STORED, CODEC_ZSTD, FLAG_CRITICAL, FLAG_HAS_CRC, FLAG_LARGE_MODE, FLAG_SAFE_TO_COPY, LARGE_UNIT, TYPE_BLOCK_INDEX, VERSION_MINOR_COMPRESSION, crc32c, getU16, getU32 } from "./format.ts";
import type { TypeDef } from "./format.ts";
import * as mem from "./memory.ts";
import { Reader as R } from "./reader.ts";
import { SAMPLE_MESH, SAMPLE_NOTE, SAMPLE_WRITER, StreamSink, assert, codeOf, enc, mustNext, pattern, sampleBytes, streamSource, test } from "./testio.ts";
import { Writer } from "./writer.ts";

const DEFS: TypeDef[] = [
	{ typeId: 1, guid: SAMPLE_MESH, name: "Mesh" },
	{ typeId: 2, guid: SAMPLE_NOTE, name: "Note" },
];

const open = (bytes: Uint8Array): R => new R(new mem.MemorySource(bytes));

test("crc32c check value and chaining", () => {
	assert.equal(crc32c(0, enc("123456789")), 0xe3069283);
	const data = pattern(1000);
	for (const cut of [0, 1, 7, 8, 9, 500, 999, 1000]) {
		assert.equal(crc32c(crc32c(0, data.subarray(0, cut)), data.subarray(cut)), crc32c(0, data));
	}
});

test("every flag combination and an empty payload round-trip", () => {
	const sink = new mem.MemorySink();
	const w = new Writer(sink, SAMPLE_WRITER, "flags", DEFS);
	const combos = [0, FLAG_CRITICAL, FLAG_SAFE_TO_COPY, FLAG_HAS_CRC, FLAG_CRITICAL | FLAG_SAFE_TO_COPY | FLAG_HAS_CRC, 0x8000 | FLAG_HAS_CRC];
	const payloads = combos.map((_, i) => pattern(i * 3, i + 1));
	combos.forEach((f, i) => w.block(1, f, payloads[i]));
	w.block(2, FLAG_HAS_CRC, new Uint8Array(0));
	w.end();
	const rd = open(sink.bytes());
	combos.forEach((f, i) => {
		const b = mustNext(rd);
		assert.equal(b.flags, f);
		assert.equal(b.offset % 8, 0);
		assert.deepEqual(rd.readPayload(b), payloads[i]);
	});
	const empty = mustNext(rd);
	assert.equal(empty.payloadLength, 0);
	assert.deepEqual(rd.readPayload(empty), new Uint8Array(0));
	assert.equal(rd.next(), null);
});

test("large_mode stores length in pages and rejects partial pages", () => {
	const w = new Writer(new mem.MemorySink(), SAMPLE_WRITER, "large", DEFS);
	assert.equal(codeOf(() => w.block(1, FLAG_LARGE_MODE, new Uint8Array(100))), "range");
	const sink = new mem.MemorySink();
	const w2 = new Writer(sink, SAMPLE_WRITER, "large", DEFS);
	const big = pattern(2 * LARGE_UNIT);
	w2.block(1, FLAG_LARGE_MODE | FLAG_HAS_CRC, big);
	w2.end();
	const bytes = sink.bytes();
	const rd = open(bytes);
	const b = mustNext(rd);
	assert.equal(b.payloadLength, big.length);
	assert.equal(bytes[b.offset + 4], 2);
	assert.deepEqual(rd.readPayload(b), big);
	assert.equal(rd.next(), null);
});

test("writeIndex without entries indexes every block and find uses it", () => {
	const sink = new mem.MemorySink();
	const w = new Writer(sink, SAMPLE_WRITER, "idx", DEFS);
	const offs: number[] = [];
	for (let i = 0; i < 5; i++) {
		offs.push(w.offset);
		w.block(i % 2 === 0 ? 1 : 2, FLAG_HAS_CRC, enc(`block ${i}`));
	}
	w.writeIndex();
	w.end();
	const rd = open(sink.bytes());
	assert.equal(rd.hasIndex, true);
	const first = mustNext(rd);
	const before = rd.offset;
	assert.deepEqual(rd.find(1), [offs[0], offs[2], offs[4]]);
	assert.deepEqual(rd.find(2), [offs[1], offs[3]]);
	assert.deepEqual(rd.find(9), []);
	assert.equal(rd.offset, before, "find keeps the reader position");
	assert.deepEqual(rd.readPayload(first), enc("block 0"));
	const at = rd.at(offs[3]);
	assert.equal(at.typeId, 2);
	assert.deepEqual(rd.readPayload(at), enc("block 3"));
	const walk = open(sink.bytes());
	const seen: number[] = [];
	for (let b = walk.next(); b !== null; b = walk.next()) {
		seen.push(b.typeId);
		walk.skipPayload(b);
	}
	assert.deepEqual(seen, [1, 2, 1, 2, 1, TYPE_BLOCK_INDEX]);
});

test("writeIndex with explicit entries writes exactly those", () => {
	const sink = new mem.MemorySink();
	const w = new Writer(sink, SAMPLE_WRITER, "idx", DEFS);
	w.block(1, 0, enc("a"));
	const keep = w.offset;
	w.block(2, 0, enc("b"));
	w.writeIndex([{ typeId: 2, offset: keep }]);
	w.end();
	const rd = open(sink.bytes());
	assert.deepEqual(rd.find(2), [keep]);
	assert.deepEqual(rd.find(1), []);
});

test("find without an index walks the file", () => {
	const rd = open(sampleBytes());
	assert.ok(!rd.hasIndex);
	assert.deepEqual(rd.find(1), [192, 248]);
	assert.deepEqual(rd.find(2), [224]);
	assert.equal(mustNext(rd).offset, 192);
});

test("find and at refuse a non-seekable source", () => {
	const rd = new R(streamSource(new mem.MemorySource(sampleBytes())));
	assert.equal(codeOf(() => rd.find(1)), "noseek");
	assert.equal(codeOf(() => rd.at(192)), "noseek");
});

test("streamPayload with a small scratch delivers every byte and verifies the CRC", () => {
	const sink = new mem.MemorySink();
	const w = new Writer(sink, SAMPLE_WRITER, "stream", DEFS);
	const data = pattern(200_000);
	w.block(1, FLAG_HAS_CRC, data);
	w.block(2, 0, enc("after"));
	w.end();
	const bytes = sink.bytes();
	for (const src of [new mem.MemorySource(bytes), streamSource(new mem.MemorySource(bytes))]) {
		const rd = new R(src);
		const b = mustNext(rd);
		const got = new Uint8Array(data.length);
		let at = 0;
		let calls = 0;
		rd.streamPayload(b, (chunk) => {
			assert.ok(chunk.length <= 7);
			got.set(chunk, at);
			at += chunk.length;
			calls++;
		}, new Uint8Array(7));
		assert.equal(at, data.length);
		assert.equal(calls, Math.ceil(data.length / 7));
		assert.deepEqual(got, data);
		assert.deepEqual(rd.readPayload(mustNext(rd)), enc("after"));
	}
	const corrupt = bytes.slice();
	corrupt[mustNext(open(bytes)).payloadOffset + 5] ^= 1;
	const bad = open(corrupt);
	assert.equal(codeOf(() => bad.streamPayload(mustNext(bad), () => {})), "crc");
});

test("readPayloadInto fills a caller buffer and rejects a short one", () => {
	const rd = open(sampleBytes());
	const b = mustNext(rd);
	assert.equal(codeOf(() => rd.readPayloadInto(b, new Uint8Array(3))), "short_buffer");
	const dst = new Uint8Array(64);
	assert.equal(rd.readPayloadInto(b, dst), 16);
	assert.deepEqual(dst.subarray(0, 16), enc("vertices-go-here"));
});

test("a non-seekable source reads a file with an index and stops at the footer", () => {
	const sink = new mem.MemorySink();
	const w = new Writer(sink, SAMPLE_WRITER, "idx", DEFS);
	w.block(1, 0, pattern(100_000));
	w.block(2, FLAG_HAS_CRC, enc("x"));
	w.writeIndex();
	w.end();
	const rd = new R(streamSource(new mem.MemorySource(sink.bytes())));
	assert.equal(rd.inputSize, -1);
	assert.ok(!rd.hasIndex);
	const types: number[] = [];
	for (let b = rd.next(); b !== null; b = rd.next()) {
		types.push(b.typeId);
		rd.readPayload(b);
	}
	assert.deepEqual(types, [1, 2, TYPE_BLOCK_INDEX]);
	assert.ok(rd.hasIndex);
});

function walkData(rd: R): Uint8Array[] {
	const got: Uint8Array[] = [];
	for (let b = rd.next(); b !== null; b = rd.next()) {
		const p = readPayloadDecompressed(rd, b);
		if (b.typeId === 1) {
			got.push(p);
		}
	}
	return got;
}

test("a file streamed to a non-seekable sink with a Block Index reads back, seekable or not", () => {
	const pipe = new StreamSink();
	const w = new Writer(pipe, SAMPLE_WRITER, "x", DEFS, { versionMinor: VERSION_MINOR_COMPRESSION });
	const payloads = [pattern(5000), enc("raw tail")];
	compressBlock(w, 1, FLAG_HAS_CRC, CODEC_ZSTD, null, payloads[0]);
	w.block(1, FLAG_HAS_CRC, payloads[1]);
	w.writeIndex();
	w.end();
	const bytes = pipe.mem.bytes();
	assert.equal(open(bytes).fileLength, null);
	const idx = getU32(bytes, bytes.length - 16);
	assert.equal(getU16(bytes, idx), TYPE_BLOCK_INDEX);

	const piped = new R(streamSource(new mem.MemorySource(bytes)));
	assert.deepEqual(walkData(piped), payloads);
	assert.ok(piped.hasIndex);
	assert.equal(piped.indexOffset, idx);
	assert.equal(piped.offset, bytes.length, "a streamed walk reads the footer");

	const seekable = open(bytes);
	assert.ok(seekable.hasIndex, "the seekable reader finds the footer before the walk");
	assert.equal(seekable.indexOffset, idx);
	assert.equal(seekable.find(1).length, 2);
	assert.deepEqual(walkData(seekable), payloads);
	assert.equal(seekable.offset, bytes.length - 16, "a seekable walk stops before the footer");
});

test("a streamed file that ends in a 16-byte block holding the footer magic keeps the block", () => {
	const pipe = new StreamSink();
	const w = new Writer(pipe, SAMPLE_WRITER, "x", DEFS);
	w.block(1, 0, enc("rezapnib"));
	w.end();
	const bytes = pipe.mem.bytes();
	for (const rd of [new R(streamSource(new mem.MemorySource(bytes))), open(bytes)]) {
		assert.deepEqual(walkData(rd), [enc("rezapnib")]);
		assert.ok(!rd.hasIndex);
	}
});

test("a non-seekable sink keeps the streaming sentinel and buffers small writes", () => {
	const pipe = new StreamSink();
	const w = new Writer(pipe, SAMPLE_WRITER, "pipe", DEFS);
	for (let i = 0; i < 200; i++) {
		w.block(2, 0, enc(`note ${i}`));
	}
	w.end();
	assert.equal(pipe.writes, 1, "small blocks reach the sink in one flush");
	const bytes = pipe.mem.bytes();
	assert.deepEqual(bytes.subarray(32, 40), new Uint8Array(8).fill(0xff));
	const rd = open(bytes);
	assert.equal(rd.fileLength, null);
	let n = 0;
	for (let b = rd.next(); b !== null; b = rd.next()) {
		assert.deepEqual(rd.readPayload(b), enc(`note ${n}`));
		n++;
	}
	assert.equal(n, 200);
});

test("a payload larger than the buffer reaches a borrowing sink as the caller's array", () => {
	const big = pattern(100_000);
	let passed: Uint8Array | null = null;
	const w = new Writer({ borrowsChunks: true, write: (src) => { if (src.length === big.length) passed = src; } }, SAMPLE_WRITER, "pipe", DEFS);
	w.block(1, 0, big);
	w.end();
	assert.equal(passed, big);
});

test("a streaming writer refuses a compressed block unless 1.1 is declared up front", () => {
	const w = new Writer(new StreamSink(), SAMPLE_WRITER, "pipe", DEFS);
	assert.equal(codeOf(() => w.blockCompressed(1, 0, CODEC_STORED, null, enc("x"), 1)), "noseek");
	const w2 = new Writer(new StreamSink(), SAMPLE_WRITER, "pipe", DEFS);
	assert.equal(codeOf(() => compressBlock(w2, 1, 0, CODEC_ZSTD, null, enc("x"))), "noseek");

	const pipe = new StreamSink();
	const w3 = new Writer(pipe, SAMPLE_WRITER, "pipe", DEFS, { versionMinor: VERSION_MINOR_COMPRESSION });
	const data = pattern(5000);
	compressBlock(w3, 1, FLAG_HAS_CRC, CODEC_ZSTD, null, data);
	w3.end();
	const bytes = pipe.mem.bytes();
	assert.equal(getU16(bytes, 14), 1);
	const rd = open(bytes);
	assert.equal(rd.fileLength, null);
	assert.deepEqual(readPayloadDecompressed(rd, mustNext(rd)), data);
});

test("a seekable writer raises version_minor only when it writes a compressed block", () => {
	const plain = new mem.MemorySink();
	const w = new Writer(plain, SAMPLE_WRITER, "v", DEFS);
	w.block(1, 0, enc("x"));
	w.end();
	assert.equal(getU16(plain.bytes(), 14), 0);
	const comp = new mem.MemorySink();
	const w2 = new Writer(comp, SAMPLE_WRITER, "v", DEFS);
	compressBlock(w2, 1, 0, CODEC_STORED, null, enc("x"));
	w2.end();
	assert.equal(getU16(comp.bytes(), 14), 1);
});

test("a writer that failed stays failed, and an ended writer refuses more blocks", () => {
	const w = new Writer(new mem.MemorySink(), SAMPLE_WRITER, "x", DEFS);
	assert.equal(codeOf(() => w.block(1, FLAG_LARGE_MODE, enc("odd"))), "range");
	assert.equal(codeOf(() => w.block(1, 0, enc("fine"))), "range");
	const w2 = new Writer(new mem.MemorySink(), SAMPLE_WRITER, "x", DEFS);
	w2.end();
	assert.equal(codeOf(() => w2.block(1, 0, enc("late"))), "format");
	const boom = new Writer({ write: () => { throw new Error("disk full"); } }, SAMPLE_WRITER, "x", DEFS);
	assert.equal(codeOf(() => boom.end()), "io");
});

test("a newer major version is refused", () => {
	const bytes = sampleBytes();
	bytes[12] = 2;
	assert.equal(codeOf(() => open(bytes)), "version");
});

test("a lookup by offset asks the source for a small read, not a full buffer", () => {
	const sink = new mem.MemorySink();
	const w = new Writer(sink, SAMPLE_WRITER, "seek", DEFS);
	const payload = pattern(100, 7);
	for (let i = 0; i < 2000; i++) {
		w.block(1, FLAG_HAS_CRC, payload);
	}
	w.writeIndex();
	w.end();

	const inner = new mem.MemorySource(sink.bytes());
	let largest = 0;
	const rd = new R({
		read: (dst) => {
			largest = Math.max(largest, dst.length);
			return inner.read(dst);
		},
		seek: (offset) => inner.seek(offset),
		size: () => inner.size(),
	});
	const offs = rd.find(1);
	assert.equal(offs.length, 2000);

	largest = 0;
	assert.deepEqual(rd.readPayload(rd.at(offs[1000])), payload);
	assert.ok(largest <= 4096, `the read after a seek asked for ${largest} bytes`);

	largest = 0;
	for (let i = 0; i < 200; i++) {
		assert.deepEqual(rd.readPayload(mustNext(rd)), payload);
	}
	assert.ok(largest > 4096, "sequential reads go back to full buffer refills");
});
