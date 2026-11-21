// Tests for table rendering, ratio formatting and the report layout.

import assert from "node:assert/strict";
import { test } from "node:test";
import { GENERATED_MARKER, formatRatio, renderProfiles, renderReport, renderTable, withRatio } from "./report.ts";
import type { OpResult, Results } from "./report.ts";
import type { Impl, Op } from "./workloads.ts";

function op(o: Op, impl: Impl, cpuS: number, rssKb: number): OpResult {
	return { op: o, impl, cpuS, userS: cpuS, sysS: 0, wallS: cpuS * 2, cpuMinS: cpuS, cpuMaxS: cpuS, maxRssKb: rssKb, throughputMiBs: 100, runs: [] };
}

function sample(): Results {
	return {
		mode: "quick",
		repeats: 1,
		environment: { workDirFilesystem: "tmpfs", cpuModel: "Test CPU", logicalCores: 4, totalMemoryBytes: 8 * 1024 ** 3, kernel: "6.0", os: "TestOS", node: "v22", cc: "cc 13", cxx: "c++ 13" },
		workloads: [
			{ name: "small", payloadBytes: 256, blocks: 2, codec: "none", fileBytes: { c: 900, go: 900 }, results: [op("write", "c", 0.1, 1024), op("write", "go", 0.18, 4096), op("read", "c", 0.2, 1024), op("read", "go", 0.1, 2048)] },
			{ name: "zstd", payloadBytes: 256, blocks: 2, codec: "zstd", fileBytes: { c: 1234567, go: 1300000 }, results: [op("write", "c", 0.1, 1024), op("write", "go", 0.1, 1024)] },
		],
		profiles: [],
	};
}

test("renderTable aligns numbers right and escapes pipes", () => {
	assert.equal(renderTable(["A", "B"], [["x|y", "1"]]), "| A | B |\n|---|---:|\n| x\\|y | 1 |");
	assert.throws(() => renderTable(["A"], [["1", "2"]]), /2 cells/);
});

test("ratio notation", () => {
	assert.equal(formatRatio(0.412, 0.2289), "1.8x");
	assert.equal(formatRatio(0.05, 0.2), "0.25x");
	assert.equal(formatRatio(1, 0), "ref 0");
	assert.equal(formatRatio(0.998, 1), "1.0x");
	assert.equal(formatRatio(0.99, 1), "0.99x");
	assert.equal(withRatio("0.412", 0.412, 0.2289, false), "0.412 (1.8x)");
	assert.equal(withRatio("0.229", 0.2289, 0.2289, true), "0.229");
});

test("report layout", () => {
	const md = renderReport({ results: sample(), impls: ["go", "c"], reference: "c", commands: ["make -C bench", "node bench/run.ts --quick"] });
	const lines = md.split("\n");
	assert.equal(lines[0], GENERATED_MARKER);
	assert.ok(md.includes("```sh\nmake -C bench\nnode bench/run.ts --quick\n```"));
	assert.ok(md.includes("| Workload / operation | C | Go |"), "columns follow C, C++, Go, TypeScript order");
	assert.ok(md.includes("| small / write | 0.100 | 0.180 (1.8x) |"));
	assert.ok(md.includes("| small / read | 0.200 | 0.100 (0.50x) |"));
	assert.ok(md.includes("| small / write | 1.0 | 4.0 (4.0x) |"));
	assert.ok(md.includes("| small / write | 0.200 s, 100 MiB/s | 0.360 s, 100 MiB/s |"));
	assert.ok(md.includes("| zstd | 1,234,567 | 1,300,000 |"));
	assert.ok(!md.includes("| small | 900"), "the size table lists codec workloads only");
	assert.ok(!md.includes("## Profiles"));
	assert.ok(!/20\d\d-\d\d-\d\d/.test(md), "no date in the report");
	assert.ok(md.endsWith("\n") && !md.endsWith("\n\n"));
});

test("profiles render one rank table per workload and operation, implementations as columns", () => {
	assert.equal(renderProfiles([]), "");
	const go = { impl: "go", workload: "small", op: "read", tool: "pprof", totalSamplesOrMs: 1500.4, unit: "ms", top: [{ name: "binpazer.(*Reader).Next", selfPercent: 31.25 }, { name: "a|b", selfPercent: 0.2 }] };
	const c = { impl: "c", workload: "small", op: "read", tool: "gprof", totalSamplesOrMs: 90, unit: "ms", top: [{ name: "binpazer_crc32c", selfPercent: 88.9 }] };
	const out = renderProfiles([go, c]);
	assert.ok(out.startsWith("## Where the CPU time goes (self time, top functions)\n\nTools: C gprof, Go pprof. Percentages are of each run's sampled CPU time."));
	assert.ok(out.includes("### small / read\n\n| Rank | C | Go |"));
	assert.ok(out.includes("| 1 | `binpazer_crc32c` (89%) | `binpazer.(*Reader).Next` (31%) |"));
	assert.ok(out.includes("| 2 |  | `a\\|b` (<1%) |"));
	assert.ok(out.includes("| sampled | 90 ms | 1500 ms |"));
	assert.throws(() => renderProfiles([go, c, { ...go, workload: "medium" }]), /expected one profile for medium \/ read \/ c, got 0/);
	const r = sample();
	assert.ok(!renderReport({ results: r, impls: ["c", "go"], reference: "c", commands: [] }).includes("Where the CPU time goes"));
	r.profiles = [c, go];
	assert.ok(renderReport({ results: r, impls: ["c", "go"], reference: "c", commands: [] }).includes("## Where the CPU time goes"));
});
