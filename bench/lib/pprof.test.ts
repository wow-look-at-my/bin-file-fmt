// Tests for the pprof decoder on hand-encoded profiles.

import assert from "node:assert/strict";
import { test } from "node:test";
import { gzipSync } from "node:zlib";
import { decodeFields, pprofSelfTimes } from "./pprof.ts";

function varint(n: number): number[] {
	const out: number[] = [];
	while (n >= 0x80) {
		out.push((n % 128) | 0x80);
		n = Math.floor(n / 128);
	}
	out.push(n);
	return out;
}

function vfield(num: number, n: number): number[] {
	return [...varint(num * 8), ...varint(n)];
}

function lfield(num: number, body: number[]): number[] {
	return [...varint(num * 8 + 2), ...varint(body.length), ...body];
}

function str(num: number, s: string): number[] {
	return lfield(num, [...Buffer.from(s, "utf8")]);
}

function packed(nums: number[]): number[] {
	return nums.flatMap(varint);
}

function sampleProfile(packedSamples: boolean): Buffer {
	const strings = ["", "samples", "count", "cpu", "nanoseconds", "main.hot", "example.com/pkg.inlined", "main.cold"];
	const sample = (locs: number[], values: number[]): number[] =>
		packedSamples ? lfield(2, [...lfield(1, packed(locs)), ...lfield(2, packed(values))]) : lfield(2, [...locs.flatMap((l) => vfield(1, l)), ...values.flatMap((v) => vfield(2, v))]);
	const bytes = [
		...vfield(9, 1795245447386058800),
		...lfield(1, [...vfield(1, 1), ...vfield(2, 2)]),
		...lfield(1, [...vfield(1, 3), ...vfield(2, 4)]),
		...sample([10, 20], [1, 10_000_000]),
		...sample([10], [2, 20_000_000]),
		...sample([20, 10], [1, 5_000_000]),
		...sample([30], [1, 3_000_000_000]),
		// A later location has an inlined frame: line[0] is the innermost function
		...lfield(4, [...vfield(1, 10), ...lfield(4, vfield(1, 2)), ...lfield(4, vfield(1, 1))]),
		...lfield(4, [...vfield(1, 20), ...lfield(4, vfield(1, 3))]),
		...lfield(4, [...vfield(1, 30), ...vfield(3, 0x401000)]),
		...lfield(5, [...vfield(1, 1), ...vfield(2, 5)]),
		...lfield(5, [...vfield(1, 2), ...vfield(2, 6)]),
		...lfield(5, [...vfield(1, 3), ...vfield(2, 7)]),
		...strings.flatMap((s) => str(6, s)),
	];
	return gzipSync(Buffer.from(bytes));
}

const WANT = new Map([
	["example.com/pkg.inlined", 30_000_000],
	["main.cold", 5_000_000],
	["0x401000", 3_000_000_000],
]);

test("self time goes to the leaf location's innermost function", () => {
	assert.deepEqual(pprofSelfTimes(sampleProfile(false)), WANT);
});

test("packed repeated fields decode the same", () => {
	assert.deepEqual(pprofSelfTimes(sampleProfile(true)), WANT);
});

test("decodeFields handles every scalar wire type", () => {
	const f = decodeFields(Buffer.from([...vfield(1, 300), 0x11, 1, 2, 3, 4, 5, 6, 7, 8, 0x1d, 1, 2, 3, 4, ...str(4, "x")]));
	assert.deepEqual(f.map((x) => [x.num, x.wire]), [[1, 0], [2, 1], [3, 5], [4, 2]]);
	assert.equal(f[0]!.int, 300);
	assert.throws(() => decodeFields(Buffer.from([0x0b])), /wire type 3/);
	assert.throws(() => decodeFields(Buffer.from([0x08, 0x80])), /truncated varint/);
});

test("a profile without cpu/nanoseconds or with dangling ids fails", () => {
	const noCpu = gzipSync(Buffer.from([...lfield(1, [...vfield(1, 1), ...vfield(2, 2)]), ...["", "samples", "count"].flatMap((s) => str(6, s))]));
	assert.throws(() => pprofSelfTimes(noCpu), /no cpu\/nanoseconds sample type \(have samples\/count\)/);
	const dangling = gzipSync(Buffer.from([...lfield(1, [...vfield(1, 1), ...vfield(2, 2)]), ...lfield(2, [...vfield(1, 9), ...vfield(2, 5)]), ...["", "cpu", "nanoseconds"].flatMap((s) => str(6, s))]));
	assert.throws(() => pprofSelfTimes(dangling), /unknown location 9/);
	assert.throws(() => pprofSelfTimes(Buffer.from("not gzip")), /incorrect header|header/);
});
