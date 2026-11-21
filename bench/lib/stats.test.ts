// Tests for medians, summaries and the measure stats line.

import assert from "node:assert/strict";
import { test } from "node:test";
import { median, parseStatsLine, summarize } from "./stats.ts";
import type { RunStats } from "./stats.ts";

test("median of odd and even counts", () => {
	assert.equal(median([3, 1, 2]), 2);
	assert.equal(median([4, 1, 3, 2]), 2.5);
	assert.equal(median([7]), 7);
	assert.throws(() => median([]), /empty/);
});

test("summarize takes medians, cpu min/max and throughput", () => {
	const run = (wall: number, user: number, sys: number, rss: number): RunStats => ({ wall_s: wall, user_s: user, sys_s: sys, max_rss_kb: rss, exit: 0 });
	const s = summarize([run(2, 1, 0.5, 100), run(1, 0.5, 0.25, 300), run(4, 2, 1, 200)], 4 * 1024 * 1024);
	assert.equal(s.wallS, 2);
	assert.equal(s.cpuS, 1.5);
	assert.equal(s.userS, 1);
	assert.equal(s.sysS, 0.5);
	assert.equal(s.cpuMinS, 0.75);
	assert.equal(s.cpuMaxS, 3);
	assert.equal(s.maxRssKb, 200);
	assert.equal(s.throughputMiBs, 2);
	assert.throws(() => summarize([], 1), /at least one/);
	assert.throws(() => summarize([run(0, 0, 0, 1)], 1), /throughput is undefined/);
});

test("parseStatsLine accepts the measure format and rejects anything else", () => {
	assert.deepEqual(parseStatsLine('{"wall_s":0.5,"user_s":0.25,"sys_s":0.125,"max_rss_kb":2048,"exit":0}\n'), { wall_s: 0.5, user_s: 0.25, sys_s: 0.125, max_rss_kb: 2048, exit: 0 });
	assert.throws(() => parseStatsLine("garbage"), /unparsable/);
	assert.throws(() => parseStatsLine('{"wall_s":1,"user_s":1,"sys_s":1,"exit":0}'), /bad "max_rss_kb"/);
	assert.throws(() => parseStatsLine('{"wall_s":-1,"user_s":1,"sys_s":1,"max_rss_kb":1,"exit":0}'), /bad "wall_s"/);
	assert.throws(() => parseStatsLine("[1]"), /not an object/);
});
