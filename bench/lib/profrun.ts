// One profiled bench run per implementation: gprof for C and C++, pprof for Go, V8 cpu-prof for TypeScript.

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

// Runs the operation once under the implementation's profiler; dir must be fresh and is the run's cwd.
export function profiledRun(progs: ProfilePrograms, impl: Impl, workload: string, op: Op, args: string[], dir: string): ProfiledRun {
	const meta = { impl, workload, op };
	if (impl === "c" || impl === "cpp") {
		const bin = progs.profBins[impl];
		const argv = [bin, op, ...args];
		const { stdout } = runDirect(argv, dir);
		const gmon = path.join(dir, "gmon.out");
		if (!fs.existsSync(gmon)) throw new Error(`${describeArgv(argv)} wrote no ${gmon}; is ${bin} built with -pg (make -C bench prof)?`);
		const flat = runDirect(["gprof", "-b", "-p", bin, gmon]).stdout;
		return { stdout, argv, summary: summarizeProfile(parseGprofFlat(flat, gmon), { ...meta, tool: "gprof" }, gmon) };
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
		return { stdout, argv, summary: summarizeProfile(ms, { ...meta, tool: "pprof" }, file) };
	}
	const argv = [process.execPath, "--cpu-prof", `--cpu-prof-dir=${dir}`, progs.bins.ts, op, ...args];
	const { stdout } = runDirect(argv, dir);
	const files = fs.readdirSync(dir).filter((f) => f.endsWith(".cpuprofile"));
	if (files.length !== 1) throw new Error(`${describeArgv(argv)} wrote ${files.length} .cpuprofile files in ${dir}, want 1`);
	const file = path.join(dir, files[0]!);
	return { stdout, argv, summary: summarizeProfile(parseCpuProfile(fs.readFileSync(file, "utf8"), file), { ...meta, tool: "v8 cpu-prof" }, file) };
}
