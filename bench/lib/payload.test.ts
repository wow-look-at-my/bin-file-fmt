// Tests for the deterministic payload generator.

import assert from "node:assert/strict";
import { test } from "node:test";
import { deflateSync } from "node:zlib";
import { PAYLOAD_SEED, generatePayload, lineText } from "./payload.ts";

function xorshift(x: number): number {
	x = (x ^ (x << 13)) >>> 0;
	x = (x ^ (x >>> 17)) >>> 0;
	return (x ^ (x << 5)) >>> 0;
}

test("the text tail is exactly 40 bytes ending in a newline", () => {
	const t = lineText();
	assert.equal(t.length, 40);
	assert.equal(t.toString("ascii"), "binpazer benchmark payload, repeated...\n");
});

test("lines hold 24 xorshift32 bytes then the text", () => {
	const p = generatePayload(256);
	assert.equal(p.length, 256);
	let x = PAYLOAD_SEED;
	for (let line = 0; line < 4; line++) {
		for (let j = 0; j < 24; j += 4) {
			x = xorshift(x);
			assert.equal(p.readUInt32LE(line * 64 + j), x);
		}
		assert.deepEqual(p.subarray(line * 64 + 24, line * 64 + 64), lineText());
	}
});

test("first bytes are pinned", () => {
	assert.equal(generatePayload(8).toString("hex"), "19460c513e552ee0");
});

test("truncation keeps the prefix and the output is deterministic", () => {
	const full = generatePayload(1000);
	assert.deepEqual(generatePayload(100), full.subarray(0, 100));
	assert.deepEqual(generatePayload(1000), full);
	assert.equal(generatePayload(0).length, 0);
	assert.throws(() => generatePayload(-1), /non-negative integer/);
});

test("the payload is roughly half compressible", () => {
	const ratio = deflateSync(generatePayload(1 << 20)).length / (1 << 20);
	assert.ok(ratio > 0.3 && ratio < 0.6, `deflate ratio ${ratio}`);
});
