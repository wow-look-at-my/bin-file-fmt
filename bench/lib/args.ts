// Command-line parsing for the benchmark driver.

import path from "node:path";
import { IMPLS, WORKLOADS, selectImpls, selectWorkloads } from "./workloads.ts";
import type { Impl, Workload } from "./workloads.ts";

export interface Options {
	quick: boolean;
	repeats: number;
	impls: Impl[];
	workloads: Workload[];
	workDir: string;
	keep: boolean;
	profile: boolean;
	render: boolean;
	profBins: { c: string; cpp: string };
	jsonPath: string;
	reportPath: string;
	bins: Record<Impl, string>;
	measureBin: string;
}

export const USAGE = `usage: node bench/run.ts [options]
	--quick              block counts / 8 (min 2), repeats default 1, outputs default into the work dir
	--repeats <n>        timed runs per operation (default 5, or 1 with --quick)
	--impls <list>       subset of ${IMPLS.join(",")} (default: all)
	--only <list>        subset of ${WORKLOADS.map((w) => w.name).join(",")} (default: all)
	--work-dir <dir>     payload and output files (default $BINPAZER_BENCH_WORK_DIR, else bench/work)
	--keep               keep the payload and .binpazer files
	--json <path>        results file (default bench/results.json)
	--report <path>      markdown report (default bench/RESULTS.md)
	--c-bin, --cpp-bin, --go-bin <path>   bench program paths
	--ts-bin <path>      the TypeScript bench .js file (run with node)
	--profile            one extra profiled run per implementation for the hot-spot tables
	--c-prof-bin, --cpp-prof-bin <path>   gprof builds (default bench/build-prof/bench_c, bench_cpp)
	--render             run nothing: rebuild the report (--report) from an existing results file (--json)
	--help               print this text`;

const VALUE_OPTIONS = ["--repeats", "--impls", "--only", "--work-dir", "--json", "--report", "--c-bin", "--cpp-bin", "--go-bin", "--ts-bin", "--c-prof-bin", "--cpp-prof-bin"];
const FLAG_OPTIONS = ["--quick", "--keep", "--profile", "--render", "--help"];

function splitList(opt: string, v: string): string[] {
	const parts = v.split(",").map((s) => s.trim());
	if (parts.some((p) => p === "")) throw new Error(`${opt}: empty name in "${v}"`);
	return parts;
}

// Returns null when --help is given. Relative paths resolve against cwd; defaults resolve against repoRoot.
export function parseArgs(argv: string[], repoRoot: string, cwd: string, env: Record<string, string | undefined> = {}): Options | null {
	const values = new Map<string, string>();
	const flags = new Set<string>();
	for (let i = 0; i < argv.length; i++) {
		const a = argv[i]!;
		const eq = a.indexOf("=");
		const name = a.startsWith("--") && eq > 0 ? a.slice(0, eq) : a;
		if (FLAG_OPTIONS.includes(name)) {
			if (name !== a) throw new Error(`${name} takes no value`);
			flags.add(name);
		} else if (VALUE_OPTIONS.includes(name)) {
			let v: string;
			if (name !== a) v = a.slice(eq + 1);
			else {
				const next = argv[i + 1];
				if (next === undefined) throw new Error(`${name} needs a value`);
				v = next;
				i++;
			}
			if (values.has(name)) throw new Error(`${name} given twice`);
			values.set(name, v);
		} else {
			throw new Error(`unknown option "${a}"; valid options: ${[...FLAG_OPTIONS, ...VALUE_OPTIONS].join(" ")}`);
		}
	}
	if (flags.has("--help")) return null;

	const quick = flags.has("--quick");
	let repeats = quick ? 1 : 5;
	const r = values.get("--repeats");
	if (r !== undefined) {
		if (!/^[1-9][0-9]*$/.test(r)) throw new Error(`--repeats must be a positive integer, got "${r}"`);
		repeats = Number(r);
	}
	const implsArg = values.get("--impls");
	const impls = selectImpls(implsArg === undefined ? [...IMPLS] : splitList("--impls", implsArg));
	const onlyArg = values.get("--only");
	const workloads = selectWorkloads(onlyArg === undefined ? null : splitList("--only", onlyArg), quick);

	const fromCwd = (p: string): string => path.resolve(cwd, p);
	const opt = (name: string, def: string): string => {
		const v = values.get(name);
		return v === undefined ? def : fromCwd(v);
	};
	const envWorkDir = env.BINPAZER_BENCH_WORK_DIR;
	const workDir = opt("--work-dir", envWorkDir !== undefined && envWorkDir !== "" ? fromCwd(envWorkDir) : path.join(repoRoot, "bench", "work"));
	const outBase = quick ? workDir : path.join(repoRoot, "bench");
	return {
		quick,
		repeats,
		impls,
		workloads,
		workDir,
		keep: flags.has("--keep"),
		profile: flags.has("--profile"),
		render: flags.has("--render"),
		profBins: {
			c: opt("--c-prof-bin", path.join(repoRoot, "bench", "build-prof", "bench_c")),
			cpp: opt("--cpp-prof-bin", path.join(repoRoot, "bench", "build-prof", "bench_cpp")),
		},
		jsonPath: opt("--json", path.join(outBase, "results.json")),
		reportPath: opt("--report", path.join(outBase, "RESULTS.md")),
		bins: {
			c: opt("--c-bin", path.join(repoRoot, "bench", "build", "bench_c")),
			cpp: opt("--cpp-bin", path.join(repoRoot, "bench", "build", "bench_cpp")),
			go: opt("--go-bin", path.join(repoRoot, "go", "build", "binpazer")),
			ts: opt("--ts-bin", path.join(repoRoot, "ts", "bench", "dist", "bench.js")),
		},
		measureBin: path.join(repoRoot, "bench", "build", "measure"),
	};
}

// The command that builds each implementation's bench program.
export const BUILD_COMMANDS: Record<Impl, string> = {
	c: "make -C bench",
	cpp: "make -C bench",
	go: "cd go && go-toolchain",
	ts: "cd ts && ts0 build",
};

// The argv prefix of an implementation's bench program; the operation and its arguments follow.
export function benchArgv(impl: Impl, bin: string, nodePath: string): string[] {
	if (impl === "go") return [bin, "bench"];
	if (impl === "ts") return [nodePath, bin];
	return [bin];
}
