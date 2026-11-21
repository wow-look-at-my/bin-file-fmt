
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { compressBlock, readPayloadDecompressed } from "./codec.ts";
import { BinpazerError, CODEC_DEFLATE, CODEC_LZ4, CODEC_STORED, CODEC_ZSTD, END_GUID, FLAG_COMPRESSED, FLAG_HAS_CRC, FLAG_LARGE_MODE, TYPE_BLOCK_TYPE_TABLE, TYPE_END, TYPE_PADDING, putU16, putU32 } from "./format.ts";
import type { Sink, Source } from "./format.ts";
import { MemoryFile, MemorySink, MemorySource } from "./memory.ts";
import { Reader } from "./reader.ts";
import type { Block } from "./reader.ts";
import { SAMPLE_NOTE, SAMPLE_WRITER, StreamSink, TESTDATA, assert, codeOf, enc, pattern, rng, sampleBytes, streamSource, test } from "./testio.ts";
import { Writer, resumeAppendWriter } from "./writer.ts";

const RECORDS = ["first record", "second record, a bit longer", ""];
const NOTE_DEFS = [{ typeId: 2, guid: SAMPLE_NOTE, name: "Note" }];
const text = (b: Uint8Array): string => new TextDecoder().decode(b);

// startSample writes the records of gen_appendable in c/gen.c.
function startSample(sink: Sink, end: boolean): Writer {
	const w = new Writer(sink, SAMPLE_WRITER, "demo", NOTE_DEFS, { appendable: true });
	for (const r of RECORDS) {
		w.block(2, 0, enc(r));
	}
	if (end) {
		w.end();
	}
	return w;
}

function appendSample(end: boolean): Uint8Array {
	const m = new MemorySink();
	startSample(m, end);
	return m.bytes();
}

interface Followed {
	got: string[];
	last: "pending" | null;
}

// followAll reads committed blocks until nextCommitted returns anything but a block.
function followAll(ar: Reader): Followed {
	const got: string[] = [];
	for (;;) {
		const b = ar.nextCommitted();
		if (b === null || b === "pending") {
			return { got, last: b };
		}
		const p = ar.readPayload(b);
		if (b.typeId !== TYPE_END) {
			got.push(text(p));
		}
	}
}

function committed(ar: Reader): Block {
	const b = ar.nextCommitted();
	assert.ok(b !== null && b !== "pending", `expected a committed block, got ${String(b)}`);
	return b;
}

// blockEnds returns the offset past each top-level block of data.
function blockEnds(data: Uint8Array): number[] {
	const ar = new Reader(new MemorySource(data));
	const ends: number[] = [];
	for (let b = ar.next(); b !== null; b = ar.next()) {
		ar.skipPayload(b);
		ends.push(ar.offset);
	}
	return ends;
}

function tableEnd(data: Uint8Array): number {
	return new Reader(new MemorySource(data)).offset;
}

function readFixture(name: string): Uint8Array {
	return new Uint8Array(readFileSync(join(TESTDATA, name)));
}

const TORN_21 = Uint8Array.from([2, 0, FLAG_HAS_CRC, 0, 0, 0, 0x10, 0, ...new Array<number>(13).fill(0xab)]);

test("the appendable writer reproduces sample-appendable.binpazer", () => {
	const want = readFixture("sample-appendable.binpazer");
	assert.deepEqual(appendSample(true), want);
	const ar = new Reader(new MemorySource(want));
	assert.equal(ar.appendable, true);
	assert.equal(ar.versionMinor, 2);
	assert.equal(ar.fileLength, want.length);
	assert.deepEqual(ar.types[ar.types.length - 1], { typeId: TYPE_END, guid: END_GUID, name: "End" });
	for (const r of RECORDS) {
		const b = committed(ar);
		assert.equal(b.typeId, 2);
		assert.equal(b.flags, FLAG_HAS_CRC);
		assert.equal(b.name, "Note");
		assert.deepEqual(ar.readPayload(b), enc(r));
	}
	const end = committed(ar);
	assert.equal(end.typeId, TYPE_END);
	assert.equal(end.flags, FLAG_HAS_CRC);
	assert.equal(end.payloadLength, 0);
	assert.equal(ar.ended, true);
	ar.skipPayload(end);
	assert.equal(ar.nextCommitted(), null);
	assert.equal(ar.next(), null);
});

test("resume reproduces sample-resumed.binpazer", () => {
	const want = readFixture("sample-resumed.binpazer");
	const open = appendSample(false);
	const f = new MemoryFile(Uint8Array.from([...open, ...TORN_21]));
	const { writer, types } = resumeAppendWriter(f);
	assert.equal(writer.sealedTail, 21);
	assert.equal(types[1].name, "Note");
	writer.block(2, 0, enc("after the crash"));
	writer.end();
	assert.deepEqual(f.bytes(), want);

	const ar = new Reader(new MemorySource(want));
	const seen: string[] = [];
	for (let b = ar.nextCommitted(); b !== null && b !== "pending"; b = ar.nextCommitted()) {
		const p = ar.readPayload(b);
		if (b.typeId === TYPE_PADDING) {
			assert.equal(b.offset, open.length);
			assert.deepEqual(p, new Uint8Array(p.length));
			seen.push("<pad>");
		} else if (b.typeId !== TYPE_END) {
			seen.push(text(p));
		}
	}
	assert.deepEqual(seen, [...RECORDS, "<pad>", "after the crash"]);
	assert.equal(ar.ended, true);
});

test("an appendable file declares 1.2, keeps the sentinel while open, and seals every block", () => {
	const data = appendSample(false);
	const ar = new Reader(new MemorySource(data));
	assert.equal(ar.appendable, true);
	assert.equal(ar.headerFlags, 1);
	assert.equal(ar.versionMinor, 2);
	assert.equal(ar.fileLength, null);
	let n = 0;
	for (let b = ar.next(); b !== null; b = ar.next()) {
		assert.ok(b.flags & FLAG_HAS_CRC, `block at ${b.offset} is not sealed`);
		ar.skipPayload(b);
		n++;
	}
	assert.equal(n, RECORDS.length);
	assert.equal(ar.ended, false);
	assert.equal(new Reader(new MemorySource(sampleBytes())).appendable, false);
});

test("end closes the file: later blocks give ended, end is idempotent, file_length is patched", () => {
	const m = new MemorySink();
	const w = startSample(m, true);
	assert.equal(codeOf(() => w.block(2, 0, enc("late"))), "ended");
	assert.equal(codeOf(() => w.blockCompressed(2, 0, CODEC_STORED, null, enc("late"), 4)), "ended");
	w.end();
	const data = m.bytes();
	const ar = new Reader(new MemorySource(data));
	assert.equal(ar.fileLength, data.length);
	assert.deepEqual(followAll(ar), { got: RECORDS, last: null });
	assert.equal(ar.ended, true);
});

test("a non-seekable sink writes a valid streaming appendable file", () => {
	const pipe = new StreamSink();
	const w = new Writer(pipe, SAMPLE_WRITER, "demo", NOTE_DEFS, { appendable: true });
	assert.equal(pipe.writes, 1, "the table reaches the sink before the first block");
	w.blockCompressed(2, 0, CODEC_STORED, null, enc("abc"), 3);
	assert.equal(pipe.writes, 2, "each block reaches the sink when it is written");
	w.end();
	const data = pipe.mem.bytes();
	const ar = new Reader(streamSource(new MemorySource(data)));
	assert.equal(ar.fileLength, null);
	let n = 0;
	for (let b = ar.next(); b !== null; b = ar.next()) {
		assert.ok(b.flags & FLAG_HAS_CRC);
		ar.readPayload(b);
		n++;
	}
	assert.equal(n, 2, "the compressed block and the End block");
	assert.equal(ar.ended, true);
	assert.equal(codeOf(() => ar.nextCommitted()), "noseek");
});

test("writeIndex is refused and the writer stays usable", () => {
	const m = new MemorySink();
	const w = startSample(m, false);
	assert.equal(codeOf(() => w.writeIndex()), "range");
	assert.equal(codeOf(() => w.writeIndex([{ typeId: 2, offset: 0 }])), "range");
	w.block(2, 0, enc("still usable"));
	w.end();
	assert.deepEqual(followAll(new Reader(new MemorySource(m.bytes()))).got, [...RECORDS, "still usable"]);
});

test("every block is sealed even with flags 0, and compressed blocks round-trip", () => {
	const f = new MemoryFile();
	const w = new Writer(f, SAMPLE_WRITER, "demo", NOTE_DEFS, { appendable: true });
	const data = pattern(20_000, 3);
	for (const id of [CODEC_STORED, CODEC_DEFLATE, CODEC_ZSTD, CODEC_LZ4]) {
		compressBlock(w, 2, 0, id, null, data);
	}
	w.block(2, FLAG_LARGE_MODE, new Uint8Array(4096));
	w.end();
	const ar = new Reader(f.view());
	for (let i = 0; i < 4; i++) {
		const b = committed(ar);
		assert.equal(b.flags, FLAG_HAS_CRC | FLAG_COMPRESSED);
		assert.deepEqual(readPayloadDecompressed(ar, b), data);
	}
	const big = committed(ar);
	assert.equal(big.flags, FLAG_HAS_CRC | FLAG_LARGE_MODE);
	ar.skipPayload(big);
	assert.equal(committed(ar).typeId, TYPE_END);
});

test("at every prefix a follower sees exactly the whole blocks and stays at the block start", () => {
	const data = appendSample(true);
	const ends = blockEnds(data);
	assert.equal(ends.length, RECORDS.length + 1);
	const first = tableEnd(data);
	for (let n = first; n <= data.length; n++) {
		const ar = new Reader(new MemorySource(data.subarray(0, n)));
		const res = followAll(ar);
		const whole = ends.filter((e) => e <= n).length;
		if (whole === ends.length) {
			assert.equal(res.last, null, `prefix ${n}`);
			assert.deepEqual(res.got, RECORDS, `prefix ${n}`);
			continue;
		}
		assert.equal(res.last, "pending", `prefix ${n}`);
		assert.equal(ar.torn, false, `prefix ${n}: a short file is in progress, not torn`);
		assert.deepEqual(res.got, RECORDS.slice(0, whole), `prefix ${n}`);
		assert.equal(ar.offset, whole === 0 ? first : ends[whole - 1], `prefix ${n}`);
	}
});

test("a follower tracks a growing file, including a block that lands in two parts", () => {
	const f = new MemoryFile();
	const w = new Writer(f, SAMPLE_WRITER, "demo", [{ typeId: 1, guid: SAMPLE_NOTE, name: "Note" }], { appendable: true });
	const ar = new Reader(f.view());
	assert.equal(ar.nextCommitted(), "pending");
	const at = ar.offset;
	assert.equal(ar.nextCommitted(), "pending");
	assert.equal(ar.offset, at, "a pending reader does not move");

	w.block(1, 0, enc("one"));
	const one = committed(ar);
	assert.equal(one.known, true);
	assert.deepEqual(ar.readPayload(one), enc("one"));
	assert.equal(ar.nextCommitted(), "pending");

	const big = new Uint8Array(3 * 65536 + 5).fill(0x78);
	compressBlock(w, 1, 0, CODEC_STORED, null, big);
	assert.deepEqual(readPayloadDecompressed(ar, committed(ar)), big);

	w.end();
	const end = committed(ar);
	assert.equal(end.typeId, TYPE_END);
	ar.skipPayload(end);
	assert.equal(ar.nextCommitted(), null);

	const whole = appendSample(true);
	const ends = blockEnds(whole);
	const grow = new MemoryFile(whole.subarray(0, ends[0] + 5));
	grow.seek(ends[0] + 5);
	const gr = new Reader(grow.view());
	assert.deepEqual(gr.readPayload(committed(gr)), enc(RECORDS[0]));
	assert.equal(gr.nextCommitted(), "pending");
	grow.write(whole.subarray(ends[0] + 5, ends[1]));
	assert.deepEqual(gr.readPayload(committed(gr)), enc(RECORDS[1]));
});

// stateRow builds a file from bytes, reads the first record, and checks the pending state at the next boundary.
function stateRow(bytes: Uint8Array, torn: boolean, repair: (f: MemoryFile) => void, label: string): void {
	const f = new MemoryFile(bytes);
	const ar = new Reader(f.view());
	assert.deepEqual(ar.readPayload(committed(ar)), enc(RECORDS[0]), label);
	const at = ar.offset;
	for (let i = 0; i < 2; i++) {
		assert.equal(ar.nextCommitted(), "pending", label);
		assert.equal(ar.torn, torn, label);
		assert.equal(ar.offset, at, `${label}: the reader stays at the block start`);
	}
	repair(f);
	const b = committed(ar);
	assert.equal(b.offset, at, label);
	assert.deepEqual(ar.readPayload(b), enc(RECORDS[1]), label);
}

test("every pending row of the state table leaves the reader in place until the bytes land", () => {
	const data = appendSample(false);
	const ends = blockEnds(data);
	const b1 = ends[0];
	const rest = (from: number) => (f: MemoryFile): void => {
		f.seek(from);
		f.write(data.subarray(from));
	};
	stateRow(data.subarray(0, b1), false, rest(b1), "end of input");
	stateRow(data.subarray(0, b1 + 5), false, rest(b1 + 5), "partial header");
	stateRow(data.subarray(0, ends[1] - 1), false, rest(ends[1] - 1), "block runs past the end");

	const zeroed = Uint8Array.from([...data.subarray(0, b1), ...new Uint8Array(64)]);
	stateRow(zeroed, false, rest(b1), "type_id 0");

	const unsealed = data.slice();
	putU16(unsealed, b1 + 2, 0);
	stateRow(unsealed, true, (f) => {
		f.seek(b1 + 2);
		f.write(data.subarray(b1 + 2, b1 + 4));
	}, "has_crc clear");

	const badSeal = data.slice();
	badSeal[b1 + 8] ^= 0xff;
	stateRow(badSeal, true, (f) => {
		f.seek(b1 + 8);
		f.write(data.subarray(b1 + 8, b1 + 9));
	}, "CRC mismatch");
});

test("nextCommitted refuses a non-appendable file and a source that cannot report its size", () => {
	const ar = new Reader(new MemorySource(sampleBytes()));
	assert.equal(codeOf(() => ar.nextCommitted()), "not_appendable");
	const src = new MemorySource(appendSample(false));
	const noSize: Source = { read: (d) => src.read(d), seek: (o) => src.seek(o) };
	assert.equal(codeOf(() => new Reader(noSize).nextCommitted()), "noseek");
});

test("find lists committed blocks only and keeps the caller's walk", () => {
	const data = appendSample(true);
	const ends = blockEnds(data);
	const ar = new Reader(new MemorySource(data.subarray(0, ends[2] - 3)));
	const at = ar.offset;
	assert.deepEqual(ar.find(2), [at, ends[0]]);
	assert.equal(ar.offset, at);
	assert.deepEqual(ar.find(TYPE_BLOCK_TYPE_TABLE), [ar.firstBlockOffset]);

	const full = new Reader(new MemorySource(data));
	assert.deepEqual(full.find(TYPE_END), [ends[2]]);
	assert.equal(full.ended, false, "a find that passes the End block does not end the caller's walk");
	assert.equal(full.at(ends[0]).typeId, 2);
});

test("resume reports the sealed tail", () => {
	const clean = appendSample(false);
	const r0 = resumeAppendWriter(new MemoryFile(clean));
	assert.equal(r0.writer.sealedTail, 0);
	assert.equal(r0.writer.offset, clean.length);
	const r1 = resumeAppendWriter(new MemoryFile(Uint8Array.from([...clean, ...new Array<number>(24).fill(0xab)])));
	assert.equal(r1.writer.sealedTail, 24);
	assert.equal(r1.writer.offset, clean.length + 24);
});

test("resume appends after the committed blocks of a clean file", () => {
	const clean = appendSample(false);
	const f = new MemoryFile(clean);
	const { writer, types } = resumeAppendWriter(f);
	assert.equal(writer.offset, clean.length);
	assert.equal(f.length, clean.length, "a clean file gets no padding");
	assert.equal(types[1].name, "Note");
	writer.block(2, 0, enc("after restart"));
	writer.end();
	assert.deepEqual(followAll(new Reader(new MemorySource(f.bytes()))), { got: [...RECORDS, "after restart"], last: null });
});

// followSkippingPadding returns the non-Padding payloads and checks that Padding is zeros.
function followSkippingPadding(data: Uint8Array): { got: string[]; reader: Reader } {
	const ar = new Reader(new MemorySource(data));
	const got: string[] = [];
	for (;;) {
		const b = ar.nextCommitted();
		if (b === null || b === "pending") {
			assert.equal(ar.torn, false);
			return { got, reader: ar };
		}
		const p = ar.readPayload(b);
		if (b.typeId === TYPE_PADDING) {
			assert.deepEqual(p, new Uint8Array(p.length), "padding is zeros");
		} else {
			got.push(text(p));
		}
	}
}

function tornHeader(len: number): Uint8Array {
	const torn = new Uint8Array(len);
	if (len >= 8) {
		putU16(torn, 0, 2);
		putU16(torn, 2, FLAG_HAS_CRC);
		putU32(torn, 4, 1 << 20);
	}
	torn.fill(0xab, 8);
	return torn;
}

test("resume seals a torn tail of any length", () => {
	const clean = appendSample(false);
	for (const tail of [1, 7, 8, 9, 15, 16, 17, 100, 4096 + 3]) {
		const f = new MemoryFile(Uint8Array.from([...clean, ...tornHeader(tail)]));
		const { writer } = resumeAppendWriter(f);
		assert.ok(writer.offset >= clean.length + tail, `tail ${tail}: padding covers every stale byte`);
		assert.equal(writer.offset % 8, 0);
		assert.deepEqual(f.bytes().subarray(0, clean.length), clean, `tail ${tail}: committed bytes are untouched`);
		writer.block(2, 0, enc("recovered"));
		const { got, reader } = followSkippingPadding(f.bytes());
		assert.deepEqual(got, [...RECORDS, "recovered"], `tail ${tail}`);
		assert.equal(reader.offset, f.length, `tail ${tail}: no stale byte is left behind`);
	}
});

test("resume hides a sealed block inside a torn tail", () => {
	const clean = appendSample(false);
	const inner = new MemorySink();
	const iw = new Writer(inner, SAMPLE_WRITER, "demo", NOTE_DEFS, { appendable: true });
	const start = iw.offset;
	iw.block(2, 0, enc("ghost"));
	const ghost = inner.bytes().subarray(start);
	const f = new MemoryFile(Uint8Array.from([...clean, ...tornHeader(8).subarray(0, 8), ...new Uint8Array(16), ...ghost]));
	const { writer } = resumeAppendWriter(f);
	writer.block(2, 0, enc("real"));
	assert.deepEqual(followSkippingPadding(f.bytes()).got, [...RECORDS, "real"]);
});

test("resume pads a long tail in several Padding blocks", () => {
	const clean = appendSample(false);
	const tail = 2 * (1 << 20) + 100;
	const f = new MemoryFile(Uint8Array.from([...clean, ...new Uint8Array(tail).fill(0xab)]));
	resumeAppendWriter(f);
	const ar = new Reader(new MemorySource(f.bytes()));
	let pads = 0;
	for (let b = ar.nextCommitted(); b !== null && b !== "pending"; b = ar.nextCommitted()) {
		if (b.typeId === TYPE_PADDING) {
			pads++;
		}
		ar.skipPayload(b);
	}
	assert.equal(pads, 3);
	assert.equal(ar.offset, f.length);
	assert.ok(f.length >= clean.length + tail);
});

test("resume refuses an ended file, a non-appendable file, garbage, and a file without seek", () => {
	assert.equal(codeOf(() => resumeAppendWriter(new MemoryFile(appendSample(true)))), "ended");
	assert.equal(codeOf(() => resumeAppendWriter(new MemoryFile(sampleBytes()))), "not_appendable");
	assert.equal(codeOf(() => resumeAppendWriter(new MemoryFile(enc("not binpazer ".repeat(8))))), "bad_magic");
	const f = new MemoryFile(appendSample(false));
	assert.equal(codeOf(() => resumeAppendWriter({ read: (d) => f.read(d), write: (s) => f.write(s) })), "noseek");
});

test("a hostile length in an appendable file is pending, never an allocation past the gate", () => {
	const data = appendSample(false);
	const b1 = blockEnds(data)[0];
	const hostile = data.slice();
	putU16(hostile, b1 + 2, FLAG_HAS_CRC | FLAG_LARGE_MODE);
	putU32(hostile, b1 + 4, 0xffffffff);
	const ar = new Reader(new MemorySource(hostile));
	ar.skipPayload(committed(ar));
	assert.equal(ar.nextCommitted(), "pending");
	assert.equal(ar.torn, false);
	const st = new Reader(streamSource(new MemorySource(hostile)), { maxAlloc: 4096 });
	const first = st.next();
	assert.ok(first !== null);
	st.skipPayload(first);
	assert.equal(codeOf(() => st.next()), "capacity");
});

test("random mutations of an appendable file give only blocks, pending, null or typed errors", () => {
	const base = appendSample(true);
	const rnd = rng(0x2545f491);
	for (let iter = 0; iter < 2000; iter++) {
		const mutated = base.slice(0, iter % 5 === 0 ? rnd(base.length) : base.length);
		const flips = 1 + rnd(3);
		for (let k = 0; k < flips && mutated.length > 0; k++) {
			mutated[rnd(mutated.length)] ^= 1 + rnd(255);
		}
		try {
			const ar = new Reader(new MemorySource(mutated), { maxAlloc: 1 << 20 });
			for (let i = 0; i < 64; i++) {
				const blk = ar.nextCommitted();
				if (blk === null || blk === "pending") {
					break;
				}
				ar.readPayload(blk);
			}
			ar.find(2);
		} catch (e) {
			assert.ok(e instanceof BinpazerError, `iteration ${iter}: ${String(e)}`);
		}
	}
});
