// Process spawning, file hashing and environment probes for the driver.

import { spawnSync } from "node:child_process";
import type { SpawnSyncReturns } from "node:child_process";
import { createHash } from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { parseStatsLine } from "./stats.ts";
import type { RunStats } from "./stats.ts";
import type { Environment } from "./report.ts";

const MAX_STDOUT = 16 * 1024 * 1024;

export interface RunOutput {
	stdout: string;
	stats: RunStats | null;
}

function describe(argv: string[]): string {
	return argv.map((a) => (/^[\w./,:=+-]+$/.test(a) ? a : JSON.stringify(a))).join(" ");
}

function check(argv: string[], r: SpawnSyncReturns<string>): string {
	if (r.error !== undefined) throw new Error(`cannot run ${describe(argv)}: ${r.error.message}`);
	if (r.signal !== null) throw new Error(`${describe(argv)} was killed by signal ${r.signal}`);
	if (r.status !== 0) throw new Error(`${describe(argv)} exited with status ${r.status}`);
	return String(r.stdout);
}

// Runs argv under the measure helper; stdout is captured and stderr passes through.
export function runMeasured(measureBin: string, statsFile: string, argv: string[]): RunOutput {
	fs.rmSync(statsFile, { force: true });
	const full = [measureBin, statsFile, ...argv];
	const r = spawnSync(full[0]!, full.slice(1), { stdio: ["ignore", "pipe", "inherit"], encoding: "utf8", maxBuffer: MAX_STDOUT });
	if (r.error !== undefined) throw new Error(`cannot run ${describe(full)}: ${r.error.message}`);
	if (!fs.existsSync(statsFile)) throw new Error(`${describe(full)} wrote no stats file (status ${r.status}, signal ${r.signal})`);
	const stats = parseStatsLine(fs.readFileSync(statsFile, "utf8"));
	fs.rmSync(statsFile);
	if (stats.exit !== 0 || r.status !== 0) throw new Error(`${describe(argv)} exited with status ${stats.exit} (measure status ${r.status})`);
	return { stdout: r.stdout, stats };
}

// Runs argv without measuring, for untimed cross-checks.
export function runDirect(argv: string[], cwd?: string): RunOutput {
	const r = spawnSync(argv[0]!, argv.slice(1), { stdio: ["ignore", "pipe", "inherit"], encoding: "utf8", maxBuffer: MAX_STDOUT, cwd });
	return { stdout: check(argv, r), stats: null };
}

// Throws naming the tool when `<tool> --version` cannot run.
export function requireTool(tool: string): void {
	const r = spawnSync(tool, ["--version"], { stdio: "ignore" });
	if (r.error !== undefined || r.status !== 0) throw new Error(`${tool} is required by --profile but \`${tool} --version\` failed: ${r.error?.message ?? `status ${r.status}`}`);
}

// Streams the file through sha256 in reads.
export function sha256File(file: string): string {
	const h = createHash("sha256");
	const fd = fs.openSync(file, "r");
	try {
		const buf = Buffer.alloc(1024 * 1024);
		for (;;) {
			const n = fs.readSync(fd, buf, 0, buf.length, null);
			if (n === 0) break;
			h.update(buf.subarray(0, n));
		}
	} finally {
		fs.closeSync(fd);
	}
	return h.digest("hex");
}

function firstLine(argv: string[]): string {
	const r = spawnSync(argv[0]!, argv.slice(1), { stdio: ["ignore", "pipe", "pipe"], encoding: "utf8" });
	const out = check(argv, r).split("\n")[0]!.trim();
	if (out === "") throw new Error(`${describe(argv)} printed nothing`);
	return out;
}

function cpuModel(): string {
	const text = fs.readFileSync("/proc/cpuinfo", "utf8");
	const m = /^model name\s*:\s*(.+)$/m.exec(text);
	if (m === null) throw new Error("/proc/cpuinfo has no \"model name\" line");
	return m[1]!.trim();
}

// PRETTY_NAME from /etc/os-release, or null when the file is not readable.
function osName(): string | null {
	let text: string;
	try {
		text = fs.readFileSync("/etc/os-release", "utf8");
	} catch {
		return null;
	}
	const m = /^PRETTY_NAME=(.*)$/m.exec(text);
	if (m === null) return null;
	return m[1]!.trim().replace(/^"(.*)"$/, "$1");
}

export function collectEnvironment(goBin: string | null, workDir: string): Environment {
	const env: Environment = {
		// Every write and read goes through this filesystem, so the report names it.
		workDirFilesystem: firstLine(["stat", "-f", "-c", "%T", workDir]),
		cpuModel: cpuModel(),
		logicalCores: os.availableParallelism(),
		totalMemoryBytes: os.totalmem(),
		kernel: os.release(),
		os: osName(),
		node: process.version,
		cc: firstLine(["cc", "--version"]),
		cxx: firstLine(["c++", "--version"]),
	};
	if (goBin !== null) env.go = firstLine([goBin, "--version"]);
	return env;
}

// Writes text atomically next to its destination, creating the directory.
export function writeFileAtomic(file: string, text: string): void {
	fs.mkdirSync(path.dirname(file), { recursive: true });
	const tmp = `${file}.tmp-${process.pid}`;
	fs.writeFileSync(tmp, text);
	fs.renameSync(tmp, file);
}

export { describe as describeArgv };
