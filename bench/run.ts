// Benchmark driver: runs the same workloads through every binpazer implementation and writes the comparison report.

import fs from "node:fs";
import path from "node:path";
import { BUILD_COMMANDS, USAGE, parseArgs } from "./lib/args.ts";
import { Bench } from "./lib/bench.ts";
import { describeArgv, writeFileAtomic } from "./lib/proc.ts";
import { renderReport } from "./lib/report.ts";
import type { Results } from "./lib/report.ts";
import { referenceImpl } from "./lib/workloads.ts";

const repoRoot = path.resolve(import.meta.dirname, "..");

function main(argv: string[]): number {
	const opts = parseArgs(argv, repoRoot, process.cwd());
	if (opts === null) {
		process.stdout.write(USAGE + "\n");
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
	const commands = [...builds, describeArgv(["node", "bench/run.ts", ...argv])];
	writeFileAtomic(opts.jsonPath, JSON.stringify(results, null, "\t") + "\n");
	writeFileAtomic(opts.reportPath, renderReport({ results, impls: opts.impls, reference: referenceImpl(opts.impls), commands }));
	process.stderr.write(`wrote ${opts.jsonPath}\nwrote ${opts.reportPath}\n`);
	return 0;
}

try {
	process.exitCode = main(process.argv.slice(2));
} catch (e) {
	process.stderr.write(`error: ${(e as Error).message}\n`);
	process.exitCode = 1;
}
