// Parser and validator for the one-line JSON result every bench program prints.

import type { Op } from "./workloads.ts";

export interface ResultLine {
	op: Op;
	blocks: number;
	bytes: number;
	checksum: string;
}

const KEYS = ["op", "blocks", "bytes", "checksum"];

// stdout must hold exactly one line with exactly those contract keys.
export function parseResultLine(stdout: string): ResultLine {
	const lines = stdout.split("\n").filter((l, i, a) => !(l === "" && i === a.length - 1));
	if (lines.length !== 1) throw new Error(`expected exactly one stdout line, got ${lines.length}: ${JSON.stringify(stdout)}`);
	const line = lines[0]!;
	let v: unknown;
	try {
		v = JSON.parse(line);
	} catch {
		throw new Error(`unparsable result line: ${JSON.stringify(line)}`);
	}
	if (typeof v !== "object" || v === null || Array.isArray(v)) throw new Error(`result line is not a JSON object: ${JSON.stringify(line)}`);
	const o = v as Record<string, unknown>;
	const keys = Object.keys(o).sort();
	if (keys.join(",") !== [...KEYS].sort().join(",")) throw new Error(`result line keys are [${keys.join(", ")}], want [${KEYS.join(", ")}]: ${line}`);
	const { op, blocks, bytes, checksum } = o;
	if (op !== "write" && op !== "read" && op !== "stream" && op !== "find") throw new Error(`result line has bad op: ${line}`);
	if (typeof blocks !== "number" || !Number.isSafeInteger(blocks) || blocks < 0) throw new Error(`result line has bad blocks: ${line}`);
	if (typeof bytes !== "number" || !Number.isSafeInteger(bytes) || bytes < 0) throw new Error(`result line has bad bytes: ${line}`);
	if (typeof checksum !== "string" || !/^[0-9a-fA-F]{8}$/.test(checksum)) throw new Error(`result line checksum is not 8 hex digits: ${line}`);
	return { op, blocks, bytes, checksum: checksum.toLowerCase() };
}

// Checks the parsed line against what the workload requires of this operation.
export function validateResultLine(r: ResultLine, op: Op, blocks: number, payloadBytes: number): void {
	const line = formatResultLine(r);
	if (r.op !== op) throw new Error(`op is "${r.op}", want "${op}": ${line}`);
	if (r.blocks !== blocks) throw new Error(`blocks is ${r.blocks}, want ${blocks}: ${line}`);
	const want = blocks * payloadBytes;
	if (r.bytes !== want) throw new Error(`bytes is ${r.bytes}, want ${want} (${blocks} x ${payloadBytes}): ${line}`);
	if (op === "write" && r.checksum !== "00000000") throw new Error(`write checksum is "${r.checksum}", want "00000000": ${line}`);
}

// Canonical form in contract key order, used to compare lines across runs and implementations.
export function formatResultLine(r: ResultLine): string {
	return JSON.stringify({ op: r.op, blocks: r.blocks, bytes: r.bytes, checksum: r.checksum });
}
