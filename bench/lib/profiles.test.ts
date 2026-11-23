// Tests for gprof and V8 profile parsing, name cleanup and the summary reduction.

import assert from "node:assert/strict";
import { test } from "node:test";
import { cleanName, parseCpuProfile, parseGprofFlat, parseSelfTimesJson, profileOps, profileWorkload, shortGoName, summarizeProfile } from "./profiles.ts";
import { addSelfTimes } from "./profrun.ts";
import { WORKLOADS } from "./workloads.ts";

const GPROF = `Flat profile:

Each sample counts as 0.01 seconds.
  %   cumulative   self              self     total
 time   seconds   seconds    calls   s/call   s/call  name
 90.00      0.90     0.90     2000     0.00     0.00  binpazer_crc32c
  5.00      0.95     0.05                             read
  3.00      0.98     0.03     8517     0.00     0.00  binpazer::detail::tramp_read(void*, void*, unsigned long)
  2.00      1.00     0.02     2000     0.00     0.00  (anonymous namespace)::Input::read_decoded(binpazer_block const&) const
  0.00      1.00     0.00        1     0.00     0.00  main

`;

const META = { impl: "c", workload: "medium", op: "read", tool: "gprof" };

test("gprof flat profile rows with and without call counts", () => {
	const m = parseGprofFlat(GPROF, "gmon.out");
	assert.equal(m.size, 5);
	assert.equal(m.get("binpazer_crc32c"), 900);
	assert.equal(m.get("read"), 50);
	assert.equal(m.get("binpazer::detail::tramp_read(void*, void*, unsigned long)"), 30);
	assert.equal(m.get("main"), 0);
	assert.throws(() => parseGprofFlat("garbage", "g"), /no gprof flat-profile header/);
	assert.throws(() => parseGprofFlat(GPROF.replace("  5.00", "  x.00"), "g"), /unparsable gprof row/);
});

test("summary: cleaned names, top 6 by self time, percentages of the total", () => {
	const s = summarizeProfile(parseGprofFlat(GPROF, "gmon.out"), META, "gmon.out");
	assert.equal(s.unit, "ms");
	assert.equal(s.tool, "gprof");
	assert.equal(s.totalSamplesOrMs, 1000);
	assert.deepEqual(s.top.map((t) => t.name), ["binpazer_crc32c", "read", "binpazer::detail::tramp_read", "(anonymous namespace)::Input::read_decoded", "main"]);
	assert.equal(s.top[0]!.selfPercent, 90);
	const many = new Map([...Array(10).keys()].map((i) => [`f${i}`, i + 1]));
	assert.equal(summarizeProfile(many, META, "x").top.length, 6);
	assert.throws(() => summarizeProfile(new Map([["main", 0]]), META, "gmon.out"), /gmon.out: the profile holds no sampled CPU time/);
	assert.throws(() => summarizeProfile(new Map(), META, "p"), /no sampled CPU time/);
});

test("cleanName strips argument lists and truncates", () => {
	assert.equal(cleanName("f(int, char const*)"), "f");
	assert.equal(cleanName("ns::g(std::function<void (int)>) const"), "ns::g");
	assert.equal(cleanName("(anonymous namespace)::h()"), "(anonymous namespace)::h");
	assert.equal(cleanName("(garbage collector)"), "(garbage collector)");
	assert.equal(cleanName("plain"), "plain");
	const long = "x".repeat(70);
	assert.equal(cleanName(long), "x".repeat(59) + "…");
	assert.equal(cleanName(long).length, 60);
	assert.equal(shortGoName("github.com/a/b/go.(*Reader).Next"), "go.(*Reader).Next");
	assert.equal(shortGoName("runtime.memmove"), "runtime.memmove");
});

test("cpuprofile self time per function name, dropping idle and root", () => {
	const prof = {
		nodes: [
			{ id: 1, callFrame: { functionName: "(root)", url: "", lineNumber: -1 } },
			{ id: 2, callFrame: { functionName: "crc32c", url: "file:///x/dist/bench.js", lineNumber: 10 } },
			{ id: 3, callFrame: { functionName: "", url: "file:///x/dist/bench.js", lineNumber: 41 } },
			{ id: 4, callFrame: { functionName: "(garbage collector)", url: "", lineNumber: -1 } },
			{ id: 5, callFrame: { functionName: "(idle)", url: "", lineNumber: -1 } },
			{ id: 6, callFrame: { functionName: "crc32c", url: "file:///x/dist/other.js", lineNumber: 3 } },
		],
		samples: [2, 2, 3, 4, 5, 1, 6],
		timeDeltas: [1000, 2000, 500, 250, 9000, 9000, 1000],
	};
	const m = parseCpuProfile(JSON.stringify(prof), "p.cpuprofile");
	assert.deepEqual(m, new Map([["crc32c", 4], ["(anonymous) bench.js:42", 0.5], ["(garbage collector)", 0.25]]));
	assert.throws(() => parseCpuProfile("{", "p"), /not valid JSON/);
	assert.throws(() => parseCpuProfile(JSON.stringify({ ...prof, timeDeltas: [1] }), "p"), /7 samples but 1 timeDeltas/);
	assert.throws(() => parseCpuProfile(JSON.stringify({ ...prof, samples: [9, 2, 2, 2, 2, 2, 2] }), "p"), /unknown node 9/);
});

test("the Python self-time JSON becomes one row per function", () => {
	const m = parseSelfTimesJson('{"format.py:crc32c": 812.5, "method \'read\' of \'_io.BufferedReader\' objects": 40}', "c.json");
	assert.deepEqual(m, new Map([["format.py:crc32c", 812.5], ["method 'read' of '_io.BufferedReader' objects", 40]]));
	assert.throws(() => parseSelfTimesJson("[", "c.json"), /c\.json: not valid JSON/);
	assert.throws(() => parseSelfTimesJson("[1]", "c.json"), /not a JSON object/);
	assert.throws(() => parseSelfTimesJson('{"f": "1"}', "c.json"), /self time of f is not a number/);
	assert.throws(() => parseSelfTimesJson("{}", "c.json"), /no rows/);
});

test("profiled pairs per workload", () => {
	const byName = (n: string) => WORKLOADS.find((w) => w.name === n)!;
	assert.deepEqual(profileOps(byName("small")), ["read"]);
	assert.deepEqual(profileOps(byName("medium")), ["write", "read"]);
	assert.deepEqual(profileOps(byName("lz4")), ["write", "read"]);
	assert.deepEqual(profileOps(byName("zstd")), []);
	for (const w of WORKLOADS) for (const op of profileOps(w)) assert.ok(w.ops.includes(op), `${w.name} times ${op}`);
	assert.equal(profileWorkload(byName("lz4")).blocks, byName("lz4").blocks * 8, "a fast workload runs longer under the sampler");
	assert.equal(profileWorkload(byName("medium")).blocks, byName("medium").blocks);
	assert.deepEqual(profileWorkload(byName("lz4")).ops, byName("lz4").ops);
});

test("self times of repeated runs sum per function", () => {
	const sum = new Map<string, number>();
	assert.equal(addSelfTimes(sum, new Map()), 0);
	assert.equal(addSelfTimes(sum, new Map([["crc", 10], ["read", 20]])), 30);
	assert.equal(addSelfTimes(sum, new Map([["crc", 10], ["write", 5]])), 45);
	assert.deepEqual([...sum.entries()].sort(), [["crc", 20], ["read", 20], ["write", 5]]);
	const s = summarizeProfile(sum, { impl: "c", workload: "small", op: "read", tool: "gprof" }, "test");
	assert.equal(s.totalSamplesOrMs, 45);
	assert.equal(s.top[0]!.selfPercent + s.top[1]!.selfPercent + s.top[2]!.selfPercent, 100);
});
