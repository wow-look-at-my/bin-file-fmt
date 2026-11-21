// Implementation and workload tables, and the selection and --quick scaling over them.

export type Impl = "c" | "cpp" | "go" | "ts";
export type Op = "write" | "read" | "stream" | "find";
export type Codec = "none" | "stored" | "deflate" | "zstd" | "lz4";

// Report column order.
export const IMPLS: readonly Impl[] = ["c", "cpp", "go", "ts"];

export const IMPL_LABELS: Record<Impl, string> = { c: "C", cpp: "C++", go: "Go", ts: "TypeScript" };

export interface Workload {
	name: string;
	payloadBytes: number;
	blocks: number;
	codec: Codec;
	ops: Op[];
}

const KiB = 1024;
const MiB = 1024 * KiB;

export const WORKLOADS: readonly Workload[] = [
	{ name: "small", payloadBytes: 256, blocks: 200000, codec: "none", ops: ["write", "read", "find"] },
	{ name: "medium", payloadBytes: 64 * KiB, blocks: 2000, codec: "none", ops: ["write", "read", "stream"] },
	{ name: "large", payloadBytes: 8 * MiB, blocks: 24, codec: "none", ops: ["write", "read", "stream"] },
	{ name: "stored", payloadBytes: MiB, blocks: 64, codec: "stored", ops: ["write", "read"] },
	{ name: "deflate", payloadBytes: MiB, blocks: 64, codec: "deflate", ops: ["write", "read"] },
	{ name: "zstd", payloadBytes: MiB, blocks: 64, codec: "zstd", ops: ["write", "read"] },
	{ name: "lz4", payloadBytes: MiB, blocks: 64, codec: "lz4", ops: ["write", "read"] },
];

// Codecs whose encoder output must be byte-identical across implementations.
export function isByteIdenticalCodec(codec: Codec): boolean {
	return codec === "none" || codec === "stored";
}

export function scaleForQuick(w: Workload): Workload {
	return { ...w, blocks: Math.max(2, Math.floor(w.blocks / 8)), ops: [...w.ops] };
}

// Returns the named workloads in table order, scaled when quick is set.
export function selectWorkloads(only: string[] | null, quick: boolean): Workload[] {
	const names = WORKLOADS.map((w) => w.name);
	if (only !== null) {
		for (const n of only) {
			if (!names.includes(n)) throw new Error(`unknown workload "${n}"; valid workloads: ${names.join(", ")}`);
		}
	}
	const picked = WORKLOADS.filter((w) => only === null || only.includes(w.name));
	return picked.map((w) => (quick ? scaleForQuick(w) : { ...w, ops: [...w.ops] }));
}

// Returns the named implementations in report column order.
export function selectImpls(names: string[]): Impl[] {
	for (const n of names) {
		if (!(IMPLS as readonly string[]).includes(n)) throw new Error(`unknown implementation "${n}"; valid implementations: ${IMPLS.join(", ")}`);
	}
	return IMPLS.filter((i) => names.includes(i));
}

// The reference implementation wrote the file every read-side operation uses.
export function referenceImpl(impls: Impl[]): Impl {
	if (impls.length === 0) throw new Error("no implementation selected");
	return impls.includes("c") ? "c" : impls[0]!;
}

// find does one lookup per block, so every operation reports the workload's block count.
export function lookupCount(w: Workload): number {
	return w.blocks;
}
