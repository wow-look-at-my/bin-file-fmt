// Tests for argument parsing, workload selection and --quick scaling.

import assert from "node:assert/strict";
import { test } from "node:test";
import { benchArgv, parseArgs } from "./args.ts";
import type { Options } from "./args.ts";
import { referenceImpl, scaleForQuick, selectImpls, selectWorkloads, WORKLOADS } from "./workloads.ts";

function parse(argv: string[]): Options {
	const o = parseArgs(argv, "/repo", "/cwd");
	assert.ok(o !== null);
	return o;
}

test("defaults select everything in full mode", () => {
	const o = parse([]);
	assert.equal(o.quick, false);
	assert.equal(o.repeats, 5);
	assert.deepEqual(o.impls, ["c", "cpp", "go", "ts"]);
	assert.deepEqual(o.workloads.map((w) => w.name), ["small", "medium", "large", "stored", "deflate", "zstd", "lz4"]);
	assert.equal(o.workloads[0]!.blocks, 200000);
	assert.equal(o.workDir, "/repo/bench/work");
	assert.equal(o.jsonPath, "/repo/bench/results.json");
	assert.equal(o.reportPath, "/repo/bench/RESULTS.md");
	assert.equal(o.bins.c, "/repo/bench/build/bench_c");
	assert.equal(o.bins.go, "/repo/go/build/binpazer");
	assert.equal(o.bins.ts, "/repo/ts/bench/dist/bench.js");
	assert.equal(o.measureBin, "/repo/bench/build/measure");
});

test("--quick scales blocks, defaults repeats to 1 and moves outputs into the work dir", () => {
	const o = parse(["--quick", "--work-dir", "w"]);
	assert.equal(o.repeats, 1);
	assert.equal(o.workDir, "/cwd/w");
	assert.equal(o.jsonPath, "/cwd/w/results.json");
	assert.equal(o.reportPath, "/cwd/w/RESULTS.md");
	assert.deepEqual(o.workloads.map((w) => w.blocks), [25000, 250, 3, 8, 8, 8, 8]);
});

test("explicit values win and --opt=value works", () => {
	const o = parse(["--quick", "--repeats=3", "--impls", "go,c", "--only=zstd,small", "--json", "r.json", "--cpp-bin", "/x/cpp", "--keep"]);
	assert.equal(o.repeats, 3);
	assert.deepEqual(o.impls, ["c", "go"]);
	assert.deepEqual(o.workloads.map((w) => w.name), ["small", "zstd"]);
	assert.equal(o.jsonPath, "/cwd/r.json");
	assert.equal(o.bins.cpp, "/x/cpp");
	assert.equal(o.keep, true);
	assert.equal(o.profile, false);
});

test("--profile and the gprof build paths", () => {
	const o = parse(["--profile", "--cpp-prof-bin", "p/cpp"]);
	assert.equal(o.profile, true);
	assert.equal(o.profBins.c, "/repo/bench/build-prof/bench_c");
	assert.equal(o.profBins.cpp, "/cwd/p/cpp");
});

test("--help returns null", () => {
	assert.equal(parseArgs(["--help"], "/repo", "/cwd"), null);
});

test("bad input fails with the valid names", () => {
	assert.throws(() => parse(["--bogus"]), /unknown option "--bogus"; valid options: .*--quick/);
	assert.throws(() => parse(["--impls", "c,rust"]), /unknown implementation "rust"; valid implementations: c, cpp, go, ts/);
	assert.throws(() => parse(["--only", "huge"]), /unknown workload "huge"; valid workloads: small, medium/);
	assert.throws(() => parse(["--repeats", "0"]), /positive integer/);
	assert.throws(() => parse(["--repeats"]), /needs a value/);
	assert.throws(() => parse(["--impls", "c,"]), /empty name/);
	assert.throws(() => parse(["--quick=1"]), /takes no value/);
	assert.throws(() => parse(["--json", "a", "--json", "b"]), /given twice/);
});

test("scaleForQuick divides by 8 with a floor of 2", () => {
	assert.equal(scaleForQuick({ ...WORKLOADS[0]!, blocks: 200000 }).blocks, 25000);
	assert.equal(scaleForQuick({ ...WORKLOADS[0]!, blocks: 24 }).blocks, 3);
	assert.equal(scaleForQuick({ ...WORKLOADS[0]!, blocks: 9 }).blocks, 2);
	assert.equal(scaleForQuick({ ...WORKLOADS[0]!, blocks: 2 }).blocks, 2);
	assert.equal(selectWorkloads(["large"], false)[0]!.blocks, 24);
});

test("implementations come back in column order and the reference prefers c", () => {
	assert.deepEqual(selectImpls(["ts", "cpp"]), ["cpp", "ts"]);
	assert.equal(referenceImpl(["c", "go"]), "c");
	assert.equal(referenceImpl(["go", "ts"]), "go");
	assert.throws(() => referenceImpl([]), /no implementation/);
});

test("bench argv prefixes", () => {
	assert.deepEqual(benchArgv("c", "/b/c", "/node"), ["/b/c"]);
	assert.deepEqual(benchArgv("go", "/b/binpazer", "/node"), ["/b/binpazer", "bench"]);
	assert.deepEqual(benchArgv("ts", "/b/bench.js", "/node"), ["/node", "/b/bench.js"]);
});
