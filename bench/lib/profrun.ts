// Profiled bench runs per implementation: gprof for C and C++, pprof for Go, V8 cpu-prof for TypeScript.

import fs from "node:fs";
import path from "node:path";
import { pprofSelfTimes } from "./pprof.ts";
import { describeArgv, runDirect } from "./proc.ts";
import { parseCpuProfile, parseGprofFlat, shortGoName, summarizeProfile } from "./profiles.ts";
import type { ProfileSummary } from "./report.ts";
import type { Impl, Op } from "./workloads.ts";

export interface ProfilePrograms {
	bins: Record<Impl, string>;
	profBins: { c: string; cpp: string };
}

export interface ProfiledRun {
	stdout: string;
	argv: string[];
	summary: ProfileSummary;
}

export const MIN_SAMPLED_MS = 250;
export const MAX_PROFILE_RUNS = 40;

interface OneRun {
	stdout: string;
	argv: string[];
	selfMs: Map<string, number>;
	tool: string;
	source: string;
}

// Adds one run's self times into the running sum and returns the sum's total.
export function addSelfTimes(sum: Map<string, number>, run: Map<string, number>): number {
	for (const [name, ms] of run) sum.set(name, (sum.get(name) ?? 0) + ms);
	let total = 0;
	for (const ms of sum.values()) total += ms;
	return total;
}

// Repeats the profiled operation until the summed profile holds enough sampled time, and summarizes the sum.
// Every repeat must print the same line. dir must be fresh; each repeat gets its own directory under it.
export function profiledRun(progs: ProfilePrograms, impl: Impl, workload: string, op: Op, args: string[], dir: string): ProfiledRun {
	const sum = new Map<string, number>();
	let first: OneRun | null = null;
	let runs = 0;
	let total = 0;
	while (runs < MAX_PROFILE_RUNS && total < MIN_SAMPLED_MS) {
		const runDir = path.join(dir, `run-${runs}`);
		fs.mkdirSync(runDir);
		const one = runOnce(progs, impl, op, args, runDir);
		if (first === null) first = one;
		else if (one.stdout !== first.stdout) {
			throw new Error(`${describeArgv(one.argv)} printed different lines across profiled runs:\n  first: ${first.stdout.trim()}\n  run ${runs + 1}: ${one.stdout.trim()}`);
		}
		total = addSelfTimes(sum, one.selfMs);
		runs++;
		fs.rmSync(runDir, { recursive: true, force: true });
	}
	const done = first!;
	const summary = summarizeProfile(sum, { impl, workload, op, tool: done.tool }, `${done.source} (${runs} runs)`);
	return { stdout: done.stdout, argv: done.argv, summary: { ...summary, runs } };
}

// Runs the operation once under the implementation's profiler; dir must be fresh and is the run's cwd.
function runOnce(progs: ProfilePrograms, impl: Impl, op: Op, args: string[], dir: string): OneRun {
	if (impl === "c" || impl === "cpp") {
		const bin = progs.profBins[impl];
		const argv = [bin, op, ...args];
		const { stdout } = runDirect(argv, dir);
		const gmon = path.join(dir, "gmon.out");
		if (!fs.existsSync(gmon)) throw new Error(`${describeArgv(argv)} wrote no ${gmon}; is ${bin} built with -pg (make -C bench prof)?`);
		const flat = runDirect(["gprof", "-b", "-p", bin, gmon]).stdout;
		return { stdout, argv, selfMs: parseGprofFlat(flat, gmon), tool: "gprof", source: gmon };
	}
	if (impl === "go") {
		const file = path.join(dir, "cpu.pprof");
		const argv = [progs.bins.go, "bench", "--cpuprofile", file, op, ...args];
		const { stdout } = runDirect(argv, dir);
		if (!fs.existsSync(file)) throw new Error(`${describeArgv(argv)} wrote no ${file}`);
		let self: Map<string, number>;
		try {
			self = pprofSelfTimes(fs.readFileSync(file));
		} catch (e) {
			throw new Error(`${file}: ${(e as Error).message}`);
		}
		const ms = new Map<string, number>();
		for (const [name, ns] of self) {
			const n = shortGoName(name);
			ms.set(n, (ms.get(n) ?? 0) + ns / 1e6);
		}
		return { stdout, argv, selfMs: ms, tool: "pprof", source: file };
	}
	const argv = [process.execPath, "--cpu-prof", `--cpu-prof-dir=${dir}`, progs.bins.ts, op, ...args];
	const { stdout } = runDirect(argv, dir);
	const files = fs.readdirSync(dir).filter((f) => f.endsWith(".cpuprofile"));
	if (files.length !== 1) throw new Error(`${describeArgv(argv)} wrote ${files.length} .cpuprofile files in ${dir}, want 1`);
	const file = path.join(dir, files[0]!);
	return { stdout, argv, selfMs: parseCpuProfile(fs.readFileSync(file, "utf8"), file), tool: "v8 cpu-prof", source: file };
}
