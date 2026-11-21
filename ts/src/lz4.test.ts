// Tests for the LZ4 frame codec against reference vectors, the lz4 CLI and hostile input.
import { test } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { lz4FrameBound, lz4FrameCompress, lz4FrameDecompress, xxh32 } from "./lz4.ts";

const vectorDir = path.join(import.meta.dirname, "..", "testdata", "lz4");

const vectors: [string, string][] = [
	["text.l1.lz4", "text.bin"],
	["text.l9.lz4", "text.bin"],
	["text.nocrc.lz4", "text.bin"],
	["linked.bd.lz4", "linked.bin"],
	["small.bx.lz4", "small.bin"],
	["small.size.lz4", "small.bin"],
	["multiblock.b4.lz4", "multiblock.bin"],
	["random.lz4", "random.bin"],
	["empty.lz4", "empty.bin"],
];

function readVector(name: string): Uint8Array {
	return new Uint8Array(fs.readFileSync(path.join(vectorDir, name)));
}

function randomBytes(n: number, seed: number): Uint8Array {
	const out = new Uint8Array(n);
	let x = seed | 0 || 1;
	for (let i = 0; i < n; i++) {
		x ^= x << 13;
		x ^= x >>> 17;
		x ^= x << 5;
		out[i] = x;
	}
	return out;
}

function compressible(n: number): Uint8Array {
	const out = new Uint8Array(n);
	for (let i = 0; i < n; i++) out[i] = (i % 97) < 60 ? (i >> 4) & 0x3f : (i * 7) & 0xff;
	return out;
}

function textLike(n: number): Uint8Array {
	const words = ["alpha", "block", "container", "the", "of", "payload", "index", "footer", "\n", "binpazer"];
	const out = new Uint8Array(n);
	const rnd = randomBytes(n, 99);
	let p = 0;
	let i = 0;
	while (p < n) {
		const w = words[rnd[i++ % n] % words.length] + " ";
		for (let j = 0; j < w.length && p < n; j++) out[p++] = w.charCodeAt(j);
	}
	return out;
}

function encode(src: Uint8Array): Uint8Array {
	const dst = new Uint8Array(lz4FrameBound(src.length));
	return dst.subarray(0, lz4FrameCompress(src, dst));
}

function decode(frame: Uint8Array, cap: number): Uint8Array {
	const dst = new Uint8Array(cap);
	return dst.subarray(0, lz4FrameDecompress(frame, dst));
}

// Returns the offset past the frame descriptor's header checksum byte.
function headerEnd(frame: Uint8Array): number {
	return (frame[4] & 0x08) !== 0 ? 15 : 7;
}

test("reference vectors decode to their original bytes", () => {
	for (const [lz, raw] of vectors) {
		const want = readVector(raw);
		const got = decode(readVector(lz), want.length);
		assert.deepEqual(got, want, lz);
	}
});

test("round trip through the TS encoder and decoder", () => {
	const inputs: [string, Uint8Array][] = [
		["empty", new Uint8Array(0)],
		["1 byte", new Uint8Array([42])],
		["12 bytes", new Uint8Array(12).fill(7)],
		["13 bytes", new Uint8Array(13).fill(7)],
		["compressible 1 MiB", compressible(1 << 20)],
		["random 1 MiB", randomBytes(1 << 20, 1)],
		["9 MiB", compressible(9 << 20)],
		["text", textLike(300000)],
	];
	for (const [name, src] of inputs) {
		const frame = encode(src);
		assert.deepEqual(decode(frame, src.length), src, name);
	}
	assert.ok(encode(compressible(1 << 20)).length < (1 << 20) / 4, "compressible input must shrink");
});

test("the lz4 CLI decodes frames the TS encoder writes", () => {
	const dir = fs.mkdtempSync(path.join(os.tmpdir(), "lz4-ts-"));
	try {
		const inputs: Uint8Array[] = [
			new Uint8Array(0),
			new Uint8Array([1, 2, 3]),
			textLike(100000),
			randomBytes(70000, 5),
			compressible(5 << 20),
		];
		inputs.forEach((src, i) => {
			const file = path.join(dir, `in${i}.lz4`);
			fs.writeFileSync(file, encode(src));
			const out = execFileSync("lz4", ["-d", "-c", file], { maxBuffer: 64 << 20 });
			assert.deepEqual(new Uint8Array(out), src, `input ${i}`);
		});
	} finally {
		fs.rmSync(dir, { recursive: true, force: true });
	}
});

test("lz4FrameBound covers the encoder output", () => {
	for (const n of [0, 1, 15, 16, 4095, 65536, 1 << 20, (4 << 20) + 1]) {
		for (const src of [randomBytes(n, n + 3), compressible(n)]) {
			const bound = lz4FrameBound(n);
			const dst = new Uint8Array(bound);
			const written = lz4FrameCompress(src, dst);
			assert.ok(written <= bound, `size ${n}`);
			assert.deepEqual(decode(dst.subarray(0, written), n), src);
		}
	}
});

test("xxh32 known answers", () => {
	assert.equal(xxh32(new Uint8Array(0)), 0x02cc5d05);
	assert.equal(xxh32(new Uint8Array(0), 1), 0x0b2cb792);
	const abc = new TextEncoder().encode("abc");
	assert.equal(xxh32(abc), 0x32d153ff);
	const long = new TextEncoder().encode("Nobody inspects the spammish repetition");
	assert.equal(xxh32(long), 0xe2293b2f);
});

test("truncation at every length throws", () => {
	for (const name of ["small.bx.lz4", "small.size.lz4"]) {
		const frame = readVector(name);
		for (let n = 0; n < frame.length; n++) {
			assert.throws(() => decode(frame.subarray(0, n), 1 << 16), /^Error: lz4: /, `${name} cut at ${n}`);
		}
	}
});

test("corrupted checksums, sizes and offsets throw", () => {
	const bx = readVector("small.bx.lz4");
	const sized = readVector("small.size.lz4");

	const badHeader = sized.slice();
	badHeader[headerEnd(sized) - 1] ^= 1;
	assert.throws(() => decode(badHeader, 1 << 16), /header checksum/);

	const badBlockSum = bx.slice();
	const blockLen = (bx[7] | (bx[8] << 8) | (bx[9] << 16)) & 0x7fffffff;
	badBlockSum[11 + blockLen] ^= 1;
	assert.throws(() => decode(badBlockSum, 1 << 16), /block checksum/);

	const badContentSum = bx.slice();
	badContentSum[bx.length - 1] ^= 1;
	assert.throws(() => decode(badContentSum, 1 << 16), /content checksum/);

	const badSize = sized.slice();
	badSize[6] ^= 1;
	badSize[14] = (xxh32(badSize.subarray(4, 14)) >>> 8) & 0xff;
	assert.throws(() => decode(badSize, 1 << 16), /content size/);

	const src = new TextEncoder().encode("hello hello hello hello lz4 frame\n");
	const frame = encode(src).slice();
	const litLen = frame[19] >>> 4;
	assert.ok(litLen < 15 && (frame[19] & 0x80000000) === 0);
	frame[20 + litLen] = 0;
	frame[21 + litLen] = 0;
	assert.throws(() => decode(frame, 1 << 16), /match offset 0/);

	const far = encode(src).slice();
	far[20 + litLen] = 0xff;
	assert.throws(() => decode(far, 1 << 16), /match offset/);
});

test("trailing garbage after the EndMark throws", () => {
	for (const name of ["small.bx.lz4", "empty.lz4"]) {
		const frame = readVector(name);
		const padded = new Uint8Array(frame.length + 1);
		padded.set(frame);
		assert.throws(() => decode(padded, 1 << 16), /trailing/);
	}
	const enc = encode(new Uint8Array(100).fill(3));
	const padded = new Uint8Array(enc.length + 4);
	padded.set(enc);
	assert.throws(() => decode(padded, 1 << 16), /^Error: lz4: /);
});

test("unsupported frame features throw", () => {
	const dict = readVector("small.size.lz4").slice();
	dict[4] |= 0x01;
	assert.throws(() => decode(dict, 1 << 16), /dictionaries are not supported/);
	const skippable = new Uint8Array([0x50, 0x2a, 0x4d, 0x18, 0, 0, 0, 0]);
	assert.throws(() => decode(skippable, 1 << 16), /skippable/);
});

test("a destination one byte too small throws RangeError", () => {
	for (const [lz, raw] of vectors) {
		const want = readVector(raw);
		if (want.length === 0) continue;
		assert.throws(() => decode(readVector(lz), want.length - 1), RangeError, lz);
	}
	const src = compressible(100000);
	assert.throws(() => decode(encode(src), src.length - 1), RangeError);
	assert.throws(() => lz4FrameCompress(src, new Uint8Array(100)), RangeError);
});
