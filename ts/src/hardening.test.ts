// Hostile input: truncation, corrupt CRCs, and oversized length fields fail with typed errors before any allocation.

import { BinpazerError, putU16, putU32, putU64 } from "./format.ts";
import { FLAG_CRITICAL, FLAG_HAS_CRC, FLAG_LARGE_MODE } from "./format.ts";
import type { Source } from "./format.ts";
import { MemorySink, MemorySource } from "./memory.ts";
import { Reader } from "./reader.ts";
import type { Block } from "./reader.ts";
import { SAMPLE_NOTE, SAMPLE_WRITER, StreamSink, assert, codeOf, enc, mustNext, sampleBytes, streamSource, test } from "./testio.ts";
import { Writer } from "./writer.ts";

const OFF_FILE_LENGTH = 32;
const OFF_NAME_LEN = 56;
const OFF_BTT_COUNT = 80;
const OFF_ENTRY0_NAME = 108;
const OFF_BLOCK1 = 192;
const OFF_PAYLOAD1 = 200;
const OFF_CRC1 = 216;

function hugePayloadSample(): Uint8Array {
	const b = sampleBytes();
	putU16(b, OFF_BLOCK1 + 2, FLAG_CRITICAL | FLAG_HAS_CRC | FLAG_LARGE_MODE);
	putU32(b, OFF_BLOCK1 + 4, 0xffffffff);
	return b;
}

function hugeBTTCountSample(): Uint8Array {
	const b = sampleBytes();
	putU32(b, OFF_BTT_COUNT, 0xffffffff);
	return b;
}

function hugeNameLenSample(): Uint8Array {
	const b = sampleBytes();
	putU32(b, OFF_NAME_LEN, 0xffffffff);
	return b;
}

function corruptCRCSample(): Uint8Array {
	const b = sampleBytes();
	b[OFF_PAYLOAD1] ^= 0xff;
	return b;
}

const seekable = (b: Uint8Array): Source => new MemorySource(b);
const streaming = (b: Uint8Array): Source => streamSource(new MemorySource(b));

// readAll opens the input and reads every payload.
function readAll(src: Source, maxAlloc?: number): void {
	const br = new Reader(src, { maxAlloc });
	for (let b = br.next(); b !== null; b = br.next()) {
		br.readPayload(b);
	}
}

// exercise drives the whole read surface, like the Go fuzz target.
function exercise(src: Source): void {
	const br = new Reader(src, { maxAlloc: 1 << 20 });
	for (let i = 0; ; i++) {
		const b = br.next();
		if (b === null) {
			break;
		}
		if (i % 2 === 0) {
			br.readPayload(b);
		} else {
			br.skipPayload(b);
		}
	}
	if (src.seek === undefined) {
		return;
	}
	for (const o of br.find(1)) {
		br.readPayload(br.at(o));
	}
}

// onlyTypedErrors runs f and fails on any exception that is not a BinpazerError.
function onlyTypedErrors(f: () => void, label: string): void {
	try {
		f();
	} catch (e) {
		assert.ok(e instanceof BinpazerError, `${label}: ${String(e)}`);
	}
}

test("the sample offsets the corruption helpers patch are where they claim", () => {
	const b = sampleBytes();
	assert.equal(b.length, 280);
	assert.equal(b[OFF_BTT_COUNT], 3);
	assert.equal(b[OFF_ENTRY0_NAME], "BlockTypeTable".length);
	assert.equal(b[OFF_BLOCK1], 1);
	assert.equal(b[OFF_BLOCK1 + 4], 16);
});

test("a prefix cut mid-structure gives truncated; any prefix ends cleanly or with a typed error", () => {
	const full = sampleBytes();
	const midStructure = new Set([0, 4, 55, 58, 90, 150, 199, 219, 279]);
	for (let n = 0; n <= full.length; n++) {
		for (const mk of [seekable, streaming]) {
			const label = `prefix ${n} (${mk === seekable ? "seekable" : "streaming"})`;
			if (midStructure.has(n)) {
				assert.equal(codeOf(() => readAll(mk(full.subarray(0, n)))), "truncated", label);
			} else {
				onlyTypedErrors(() => readAll(mk(full.subarray(0, n))), label);
			}
		}
	}
	for (const mk of [seekable, streaming]) {
		const br = new Reader(mk(full.subarray(0, 224)));
		br.readPayload(mustNext(br));
		assert.equal(br.next(), null, "a cut at a block boundary ends the walk cleanly");
	}
});

test("a flipped payload or CRC byte gives crc", () => {
	for (const at of [OFF_PAYLOAD1, OFF_CRC1]) {
		const b = sampleBytes();
		b[at] ^= 1;
		const br = new Reader(seekable(b));
		const blk = mustNext(br);
		assert.equal(codeOf(() => br.readPayload(blk)), "crc");
	}
});

test("bad magic and bad signature give bad_magic", () => {
	const b = sampleBytes();
	b[0] = 0x58;
	assert.equal(codeOf(() => new Reader(seekable(b))), "bad_magic");
	const s = sampleBytes();
	s[8] = 0;
	assert.equal(codeOf(() => new Reader(seekable(s))), "bad_magic");
});

test("a huge payload claim is refused before the payload is allocated", () => {
	const br = new Reader(seekable(hugePayloadSample()));
	assert.equal(codeOf(() => br.next()), "truncated");
	const capped = new Reader(streaming(hugePayloadSample()), { maxAlloc: 4096 });
	assert.equal(codeOf(() => capped.next()), "capacity");
});

test("a huge block type table entry count gives capacity", () => {
	assert.equal(codeOf(() => new Reader(seekable(hugeBTTCountSample()))), "capacity");
	assert.equal(codeOf(() => new Reader(streaming(hugeBTTCountSample()))), "capacity");
});

test("a huge writer name length is bounded by the input size or by maxAlloc", () => {
	assert.equal(codeOf(() => new Reader(seekable(hugeNameLenSample()))), "truncated");
	assert.equal(codeOf(() => new Reader(streaming(hugeNameLenSample()))), "capacity");
	const b = sampleBytes();
	putU32(b, OFF_NAME_LEN, 100_000);
	assert.equal(codeOf(() => new Reader(streaming(b), { maxAlloc: 1024 })), "capacity");
});

test("a block type table name that overruns the table gives capacity", () => {
	const b = sampleBytes();
	putU32(b, OFF_ENTRY0_NAME, 0xffff);
	assert.equal(codeOf(() => new Reader(seekable(b))), "capacity");
});

test("maxAlloc caps a streaming input and the default cap admits normal files", () => {
	assert.equal(codeOf(() => new Reader(streaming(sampleBytes()), { maxAlloc: 64 })), "capacity");
	const br = new Reader(streaming(sampleBytes()));
	assert.equal(br.inputSize, -1);
	let n = 0;
	for (let b = br.next(); b !== null; b = br.next()) {
		br.skipPayload(b);
		n++;
	}
	assert.equal(n, 3);
});

test("a lying file_length degrades to no footer and a walk that ends at the input", () => {
	const cases: [string, number, number][] = [
		["296", 0, 296],
		["1<<62", 0x40000000, 0],
		["sentinel - 1", 0xffffffff, 0xfffffffe],
	];
	for (const [label, hi, lo] of cases) {
		for (const mk of [seekable, streaming]) {
			const b = sampleBytes();
			putU32(b, OFF_FILE_LENGTH, lo);
			putU32(b, OFF_FILE_LENGTH + 4, hi);
			const br = new Reader(mk(b));
			assert.equal(br.hasIndex, false, label);
			assert.ok(br.fileLength !== null && br.fileLength >= 296, label);
			let n = 0;
			for (let blk = br.next(); blk !== null; blk = br.next()) {
				br.skipPayload(blk);
				n++;
			}
			assert.equal(n, 3, label);
			assert.equal(br.offset, b.length, label);
		}
	}
});

test("a footer offset past the safe range opens like Go and fails only in find", () => {
	const m = new MemorySink();
	const w = new Writer(m, SAMPLE_WRITER, "idx", []);
	w.block(1, 0, enc("x"));
	w.writeIndex();
	w.end();
	const b = m.bytes();
	putU32(b, b.length - 12, 0x40000000);
	const br = new Reader(seekable(b));
	assert.equal(br.hasIndex, true);
	assert.equal(codeOf(() => br.find(1)), "range");
	const walk = new Reader(seekable(b));
	let n = 0;
	for (let blk = walk.next(); blk !== null; blk = walk.next()) {
		walk.skipPayload(blk);
		n++;
	}
	assert.equal(n, 2);
});

test("a caller-made block cannot drive an unbounded allocation", () => {
	const br = new Reader(seekable(sampleBytes()));
	const fake: Block = { typeId: 1, flags: 0, payloadLength: 2 ** 40, payloadOffset: 0, offset: 0, guid: null, known: false, name: "" };
	assert.equal(codeOf(() => br.readPayload(fake)), "truncated");
	const capped = new Reader(streaming(sampleBytes()), { maxAlloc: 1024 });
	assert.equal(codeOf(() => capped.readPayload(fake)), "capacity");
});

test("a file cut inside a block's padding reports truncated and stops at end of input", () => {
	const pipe = new StreamSink();
	const w = new Writer(pipe, SAMPLE_WRITER, "demo", [{ typeId: 2, guid: SAMPLE_NOTE, name: "Note" }]);
	w.block(2, 0, enc("abc"));
	w.end();
	const all = pipe.mem.bytes();
	const cut = all.subarray(0, all.length - 5);
	for (const mk of [seekable, streaming]) {
		const br = new Reader(mk(cut));
		const b = mustNext(br);
		assert.equal(codeOf(() => br.skipPayload(b)), "truncated");
		assert.equal(br.offset, cut.length);
	}
});

test("a footer that points past the input gives truncated from find", () => {
	const m = new MemorySink();
	const w = new Writer(m, SAMPLE_WRITER, "idx", []);
	w.block(1, 0, enc("x"));
	w.writeIndex();
	w.end();
	const b = m.bytes();
	putU64(b, b.length - 16, 1_000_000);
	const br = new Reader(seekable(b));
	assert.equal(br.hasIndex, true);
	assert.equal(codeOf(() => br.find(1)), "truncated");
	assert.equal(codeOf(() => br.at(1_000_000)), "truncated");
});

test("the fuzz seeds raise only typed errors", () => {
	const sample = sampleBytes();
	const seeds = [sample, enc("binpazer"), hugePayloadSample(), hugeBTTCountSample(), hugeNameLenSample(), corruptCRCSample()];
	for (const n of [4, 56, 60, 100, 150, 224, 279]) {
		seeds.push(sample.slice(0, n));
	}
	seeds.forEach((s, i) => {
		onlyTypedErrors(() => exercise(seekable(s)), `seed ${i} seekable`);
		onlyTypedErrors(() => exercise(streaming(s)), `seed ${i} streaming`);
	});
	exercise(seekable(sample));
});

test("random mutations of an indexed file raise only typed errors", () => {
	const m = new MemorySink();
	const w = new Writer(m, SAMPLE_WRITER, "fuzz", [{ typeId: 1, guid: SAMPLE_NOTE, name: "Note" }]);
	w.block(1, FLAG_HAS_CRC, enc("first payload"));
	w.block(2, 0, enc("second"));
	w.block(1, FLAG_LARGE_MODE, new Uint8Array(4096));
	w.writeIndex();
	w.end();
	const base = m.bytes();
	let x = 0x9e3779b9;
	const rnd = (n: number): number => {
		x ^= x << 13;
		x ^= x >>> 17;
		x ^= x << 5;
		return (x >>> 0) % n;
	};
	for (let iter = 0; iter < 3000; iter++) {
		const b = base.slice(0, iter % 7 === 0 ? rnd(base.length) : base.length);
		const flips = 1 + rnd(4);
		for (let k = 0; k < flips && b.length > 0; k++) {
			b[rnd(b.length)] = rnd(256);
		}
		onlyTypedErrors(() => exercise(seekable(b)), `iteration ${iter} seekable`);
		onlyTypedErrors(() => exercise(streaming(b)), `iteration ${iter} streaming`);
	}
});
