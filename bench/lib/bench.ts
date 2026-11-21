// The run plan: timed writes, agreement checks and timed reads across the selected implementations.

import fs from "node:fs";
import path from "node:path";
import { BUILD_COMMANDS, benchArgv } from "./args.ts";
import type { Options } from "./args.ts";
import { generatePayload } from "./payload.ts";
import { collectEnvironment, describeArgv, runDirect, runMeasured, sha256File } from "./proc.ts";
import { formatMiB } from "./report.ts";
import type { OpResult, Results, WorkloadResult } from "./report.ts";
import { formatResultLine, parseResultLine, validateResultLine } from "./resultline.ts";
import { summarize } from "./stats.ts";
import type { RunStats } from "./stats.ts";
import { IMPL_LABELS, isByteIdenticalCodec, lookupCount, referenceImpl } from "./workloads.ts";
import type { Impl, Op, Workload } from "./workloads.ts";

// Throws naming the missing file and the command that builds it.
export function checkPrograms(opts: Options): void {
	if (!fs.existsSync(opts.measureBin)) throw new Error(`measure helper not found: ${opts.measureBin}; build it with: make -C bench`);
	for (const impl of opts.impls) {
		const bin = opts.bins[impl];
		if (!fs.existsSync(bin)) throw new Error(`${IMPL_LABELS[impl]} bench program not found: ${bin}; build it with: ${BUILD_COMMANDS[impl]}`);
	}
	if (!opts.profile) return;
	const gprofImpls = opts.impls.filter((i): i is "c" | "cpp" => i === "c" || i === "cpp");
	for (const impl of gprofImpls) {
		const bin = opts.profBins[impl];
		if (!fs.existsSync(bin)) throw new Error(`${IMPL_LABELS[impl]} gprof build not found: ${bin}; build it with: make -C bench prof`);
	}
	if (gprofImpls.length > 0) requireTool("gprof");
}

export class Bench {
	readonly opts: Options;
	readonly made = new Set<string>();
	readonly profiles: ProfileSummary[] = [];
	readonly statsFile: string;

	constructor(opts: Options) {
		this.opts = opts;
		this.statsFile = path.join(opts.workDir, `.measure-${process.pid}.json`);
	}

	argv(impl: Impl, op: Op, args: string[]): string[] {
		return [...benchArgv(impl, this.opts.bins[impl], process.execPath), op, ...args];
	}

	opArgs(w: Workload, op: Op, file: string, payload: string): string[] {
		if (op === "write") return [file, payload, String(w.blocks), w.codec];
		if (op === "find") return [file, String(lookupCount(w))];
		return [file];
	}

	// Runs the operation <repeats> times under measure; every repeat must print the same valid line.
	timed(w: Workload, op: Op, impl: Impl, args: string[]): { line: string; runs: RunStats[] } {
		const argv = this.argv(impl, op, args);
		let first: string | null = null;
		const runs: RunStats[] = [];
		for (let i = 0; i < this.opts.repeats; i++) {
			const out = runMeasured(this.opts.measureBin, this.statsFile, argv);
			const line = this.checkedLine(w, op, out.stdout, argv);
			if (first === null) first = line;
			else if (line !== first) throw new Error(`${describeArgv(argv)} printed different lines across repeats:\n  run 1: ${first}\n  run ${i + 1}: ${line}`);
			runs.push(out.stats!);
		}
		return { line: first!, runs };
	}

	checkedLine(w: Workload, op: Op, stdout: string, argv: string[]): string {
		try {
			const r = parseResultLine(stdout);
			validateResultLine(r, op, w.blocks, w.payloadBytes);
			return formatResultLine(r);
		} catch (e) {
			throw new Error(`${describeArgv(argv)}: ${(e as Error).message}`);
		}
	}

	record(w: Workload, op: Op, impl: Impl, runs: RunStats[]): OpResult {
		const s = summarize(runs, w.blocks * w.payloadBytes);
		process.stderr.write(`[${w.name}] ${op} ${IMPL_LABELS[impl]}: cpu ${s.cpuS.toFixed(3)} s, wall ${s.wallS.toFixed(3)} s, rss ${formatMiB(s.maxRssKb)} MiB (median of ${runs.length})\n`);
		return { op, impl, ...s, runs };
	}

	runWorkload(w: Workload): WorkloadResult {
		const { impls, workDir } = this.opts;
		const ref = referenceImpl(impls);
		const payload = path.join(workDir, `${w.name}.payload`);
		this.made.add(payload);
		fs.writeFileSync(payload, generatePayload(w.payloadBytes));
		const fileOf = (impl: Impl): string => path.join(workDir, `${w.name}.${impl}.binpazer`);
		const results: OpResult[] = [];
		const fileBytes: Partial<Record<Impl, number>> = {};

		for (const impl of impls) {
			this.made.add(fileOf(impl));
			const t = this.timed(w, "write", impl, this.opArgs(w, "write", fileOf(impl), payload));
			results.push(this.record(w, "write", impl, t.runs));
			fileBytes[impl] = fs.statSync(fileOf(impl)).size;
		}
		this.checkAgreement(w, fileOf);

		for (const op of w.ops.filter((o) => o !== "write")) {
			let refLine: string | null = null;
			for (const impl of impls) {
				const t = this.timed(w, op, impl, this.opArgs(w, op, fileOf(ref), payload));
				if (refLine === null) refLine = t.line;
				else if (t.line !== refLine) throw new Error(`[${w.name}] ${op} of ${fileOf(ref)} disagrees:\n  ${IMPL_LABELS[impls[0]!]}: ${refLine}\n  ${IMPL_LABELS[impl]}: ${t.line}`);
				results.push(this.record(w, op, impl, t.runs));
			}
		}
		if (!this.opts.keep) this.removeMade();
		return { name: w.name, payloadBytes: w.payloadBytes, blocks: w.blocks, codec: w.codec, fileBytes, results };
	}

	// Byte identity for codecs that require it; otherwise every implementation reads every file to the same line.
	checkAgreement(w: Workload, fileOf: (impl: Impl) => string): void {
		const { impls } = this.opts;
		if (isByteIdenticalCodec(w.codec)) {
			const base = impls[0]!;
			const baseHash = sha256File(fileOf(base));
			for (const impl of impls.slice(1)) {
				const h = sha256File(fileOf(impl));
				if (h !== baseHash) throw new Error(`[${w.name}] written files differ (codec ${w.codec} must be byte-identical): ${fileOf(base)} sha256 ${baseHash}, ${fileOf(impl)} sha256 ${h}`);
			}
			return;
		}
		let first: { writer: Impl; reader: Impl; line: string } | null = null;
		for (const writer of impls) {
			for (const reader of impls) {
				const argv = this.argv(reader, "read", [fileOf(writer)]);
				let line: string;
				try {
					line = this.checkedLine(w, "read", runDirect(argv).stdout, argv);
				} catch (e) {
					throw new Error(`[${w.name}] ${IMPL_LABELS[reader]} cannot read the file ${IMPL_LABELS[writer]} wrote (${fileOf(writer)}): ${(e as Error).message}`);
				}
				if (first === null) first = { writer, reader, line };
				else if (line !== first.line) {
					throw new Error(`[${w.name}] cross-read mismatch:\n  ${IMPL_LABELS[first.reader]} reading ${IMPL_LABELS[first.writer]}'s file: ${first.line}\n  ${IMPL_LABELS[reader]} reading ${IMPL_LABELS[writer]}'s file (${fileOf(writer)}): ${line}`);
				}
			}
		}
		process.stderr.write(`[${w.name}] cross-read: ${impls.length * impls.length} reader/writer pairs agree\n`);
	}

	removeMade(): void {
		for (const f of this.made) fs.rmSync(f, { force: true });
		this.made.clear();
	}

	run(): Results {
		checkPrograms(this.opts);
		const environment = collectEnvironment(this.opts.impls.includes("go") ? this.opts.bins.go : null);
		const workloads = this.opts.workloads.map((w) => this.runWorkload(w));
		return { mode: this.opts.quick ? "quick" : "full", repeats: this.opts.repeats, environment, workloads, profiles: [] };
	}
}
