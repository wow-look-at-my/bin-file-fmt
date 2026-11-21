// Benchmark driver: runs the same workloads through every binpazer implementation and writes the comparison report.

import fs from "node:fs";
import path from "node:path";
import { BUILD_COMMANDS, USAGE, parseArgs } from "./lib/args.ts";
import { Bench } from "./lib/bench.ts";
import { describeArgv, writeFileAtomic } from "./lib/proc.ts";
import { renderReport } from "./lib/report.ts";
import type { Results, StoredResults } from "./lib/report.ts";
import { referenceImpl } from "./lib/workloads.ts";

const repoRoot = path.resolve(import.meta.dirname, "..");

function main(argv: string[]): number {
	const opts = parseArgs(argv, repoRoot, process.cwd());
	if (opts === null) {
		process.stdout.write(USAGE + "\n");
		return 0;
	}
	if (opts.render) {
		const stored = JSON.parse(fs.readFileSync(opts.jsonPath, "utf8")) as Partial<StoredResults>;
		if (!Array.isArray(stored.impls) || !Array.isArray(stored.commands) || typeof stored.reference !== "string" || !Array.isArray(stored.workloads)) {
			throw new Error(`${opts.jsonPath} does not hold a full results file with the report inputs (impls, reference, commands)`);
		}
		const full = stored as StoredResults;
		writeFileAtomic(opts.reportPath, renderReport({ results: full, impls: full.impls, reference: full.reference, commands: full.commands }));
		process.stderr.write(`wrote ${opts.reportPath}\n`);
		return 0;
	}
	const createdWorkDir = !fs.existsSync(opts.workDir);
	fs.mkdirSync(opts.workDir, { recursive: true });
	const removeEmptyWorkDir = (): void => {
		if (createdWorkDir && fs.readdirSync(opts.workDir).length === 0) fs.rmdirSync(opts.workDir);
	};
	const bench = new Bench(opts);
	let results: Results;
	try {
		results = bench.run();
	} catch (e) {
		if (bench.made.size > 0) process.stderr.write(`work files are left in ${opts.workDir} for inspection\n`);
		else removeEmptyWorkDir();
		throw e;
	}
	if (!opts.keep) bench.removeMade();
	removeEmptyWorkDir();

	const profBuild = opts.profile && (opts.impls.includes("c") || opts.impls.includes("cpp")) ? ["make -C bench prof"] : [];
	const builds = [...new Set(["make -C bench", ...profBuild, ...opts.impls.map((i) => BUILD_COMMANDS[i])])];
	// A subshell keeps each `cd` from leaking into the next command.
	const commands = [...builds.map((c) => (c.startsWith("cd ") ? `(${c})` : c)),describeArgv(["node", "bench/run.ts", ...argv])];
	const stored: StoredResults = { ...results, impls: opts.impls, reference: referenceImpl(opts.impls), commands };
	writeFileAtomic(opts.jsonPath, JSON.stringify(stored, null, "\t") + "\n");
	writeFileAtomic(opts.reportPath, renderReport({ results, impls: stored.impls, reference: stored.reference, commands }));
	process.stderr.write(`wrote ${opts.jsonPath}\nwrote ${opts.reportPath}\n`);
	return 0;
}

try {
	process.exitCode = main(process.argv.slice(2));
} catch (e) {
	process.stderr.write(`error: ${(e as Error).message}\n`);
	process.exitCode = 1;
}
