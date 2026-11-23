// Profile parsing for gprof flat profiles and V8 .cpuprofile files, and the reduction to ProfileSummary.

import path from "node:path";
import type { ProfileSummary } from "./report.ts";
import type { Op, Workload } from "./workloads.ts";

export const TOP_FUNCTIONS = 6;
export const MAX_NAME = 60;

// The fixed (workload, operation) pairs that --profile covers.
export const PROFILE_PAIRS: readonly { workload: string; op: Op }[] = [
	{ workload: "small", op: "read" },
	{ workload: "medium", op: "write" },
	{ workload: "medium", op: "read" },
	{ workload: "lz4", op: "write" },
	{ workload: "lz4", op: "read" },
];

export function profileOps(w: Workload): Op[] {
	return PROFILE_PAIRS.filter((p) => p.workload === w.name).map((p) => p.op);
}

// Removes a trailing C++ argument list (and cv or ref qualifiers after it), then truncates with an ellipsis.
export function cleanName(raw: string): string {
	let name = raw.trim().replace(/(\s*(const|volatile|&&|&))+$/, "");
	if (name.endsWith(")")) {
		let depth = 0;
		for (let i = name.length - 1; i >= 0; i--) {
			const ch = name[i];
			if (ch === ")") depth++;
			else if (ch === "(") {
				depth--;
				if (depth === 0) {
					if (i > 0) name = name.slice(0, i);
					break;
				}
			}
		}
	}
	return name.length > MAX_NAME ? name.slice(0, MAX_NAME - 1) + "…" : name;
}

// Go symbols carry their full import path; the last path element is enough to read them.
export function shortGoName(name: string): string {
	const slash = name.lastIndexOf("/");
	return slash < 0 ? name : name.slice(slash + 1);
}

// Builds the summary from self milliseconds per function; an empty or zero profile is an error.
export function summarizeProfile(selfMs: Map<string, number>, meta: { impl: string; workload: string; op: string; tool: string }, source: string): ProfileSummary {
	const merged = new Map<string, number>();
	for (const [name, ms] of selfMs) {
		if (!Number.isFinite(ms) || ms < 0) throw new Error(`${source}: bad self time ${ms} for ${name}`);
		const n = cleanName(name);
		merged.set(n, (merged.get(n) ?? 0) + ms);
	}
	let total = 0;
	for (const ms of merged.values()) total += ms;
	if (total <= 0) throw new Error(`${source}: the profile holds no sampled CPU time`);
	const top = [...merged.entries()]
		.sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1))
		.slice(0, TOP_FUNCTIONS)
		.map(([name, ms]) => ({ name, selfPercent: (ms / total) * 100 }));
	return { ...meta, totalSamplesOrMs: Math.round(total * 1000) / 1000, unit: "ms", top };
}

const GPROF_ROW = /^\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)\s+(?:\d+\s+[\d.]+\s+[\d.]+\s+)?(\S.*)$/;

// Parses `gprof -b -p` output into self milliseconds per function.
export function parseGprofFlat(text: string, source: string): Map<string, number> {
	const lines = text.split("\n");
	const header = lines.findIndex((l) => /^\s*time\s+seconds\s+seconds\b.*\bname\s*$/.test(l));
	if (header < 0) throw new Error(`${source}: no gprof flat-profile header in: ${JSON.stringify(text.slice(0, 200))}`);
	const out = new Map<string, number>();
	for (const line of lines.slice(header + 1)) {
		if (line.trim() === "") break;
		const m = GPROF_ROW.exec(line);
		if (m === null) throw new Error(`${source}: unparsable gprof row: ${JSON.stringify(line)}`);
		const name = m[4]!.trim();
		out.set(name, (out.get(name) ?? 0) + Number(m[3]) * 1000);
	}
	if (out.size === 0) throw new Error(`${source}: gprof flat profile has no rows`);
	return out;
}

// Parses the JSON object that the Python bench program writes: self milliseconds per function name.
export function parseSelfTimesJson(text: string, source: string): Map<string, number> {
	let v: unknown;
	try {
		v = JSON.parse(text);
	} catch {
		throw new Error(`${source}: not valid JSON`);
	}
	if (typeof v !== "object" || v === null || Array.isArray(v)) throw new Error(`${source}: not a JSON object`);
	const out = new Map<string, number>();
	for (const [name, ms] of Object.entries(v)) {
		if (typeof ms !== "number") throw new Error(`${source}: self time of ${name} is not a number`);
		out.set(name, ms);
	}
	if (out.size === 0) throw new Error(`${source}: the profile has no rows`);
	return out;
}

interface CpuProfileNode {
	id: number;
	callFrame: { functionName: string; url: string; lineNumber: number };
}

const DROPPED_V8 = new Set(["(idle)", "(root)"]);

// Parses a V8 .cpuprofile into self milliseconds per function name.
export function parseCpuProfile(text: string, source: string): Map<string, number> {
	let v: unknown;
	try {
		v = JSON.parse(text);
	} catch {
		throw new Error(`${source}: not valid JSON`);
	}
	if (typeof v !== "object" || v === null) throw new Error(`${source}: not a JSON object`);
	const o = v as { nodes?: unknown; samples?: unknown; timeDeltas?: unknown };
	if (!Array.isArray(o.nodes) || !Array.isArray(o.samples) || !Array.isArray(o.timeDeltas)) throw new Error(`${source}: missing nodes, samples or timeDeltas`);
	if (o.samples.length !== o.timeDeltas.length) throw new Error(`${source}: ${o.samples.length} samples but ${o.timeDeltas.length} timeDeltas`);
	const names = new Map<number, string>();
	for (const raw of o.nodes as unknown[]) {
		const n = raw as CpuProfileNode;
		if (typeof n !== "object" || n === null || typeof n.id !== "number" || typeof n.callFrame !== "object" || n.callFrame === null) throw new Error(`${source}: malformed node ${JSON.stringify(raw)}`);
		const cf = n.callFrame;
		const name = cf.functionName !== "" ? cf.functionName : `(anonymous) ${cf.url === "" ? "?" : path.basename(cf.url)}:${cf.lineNumber + 1}`;
		names.set(n.id, name);
	}
	const out = new Map<string, number>();
	for (let i = 0; i < o.samples.length; i++) {
		const id = o.samples[i] as unknown;
		const dt = o.timeDeltas[i] as unknown;
		if (typeof id !== "number" || typeof dt !== "number") throw new Error(`${source}: sample ${i} is not numeric`);
		const name = names.get(id);
		if (name === undefined) throw new Error(`${source}: sample ${i} names unknown node ${id}`);
		if (DROPPED_V8.has(name)) continue;
		// V8 can emit a small negative delta when it reorders samples; it counts as zero.
		out.set(name, (out.get(name) ?? 0) + Math.max(0, dt) / 1000);
	}
	return out;
}
