// Run statistics: the measure helper's stats line, medians and per-operation summaries.

export interface RunStats {
	wall_s: number;
	user_s: number;
	sys_s: number;
	max_rss_kb: number;
	exit: number;
}

export interface Summary {
	cpuS: number;
	userS: number;
	sysS: number;
	wallS: number;
	cpuMinS: number;
	cpuMaxS: number;
	maxRssKb: number;
	throughputMiBs: number;
}

const STAT_FIELDS = ["wall_s", "user_s", "sys_s", "max_rss_kb", "exit"] as const;

// Parses one stats line from bench/build/measure; any missing or non-numeric field is an error.
export function parseStatsLine(text: string): RunStats {
	const line = text.trim();
	let v: unknown;
	try {
		v = JSON.parse(line);
	} catch {
		throw new Error(`measure wrote an unparsable stats line: ${JSON.stringify(line)}`);
	}
	if (typeof v !== "object" || v === null || Array.isArray(v)) throw new Error(`measure stats line is not an object: ${JSON.stringify(line)}`);
	const o = v as Record<string, unknown>;
	const num = (k: (typeof STAT_FIELDS)[number]): number => {
		const x = o[k];
		if (typeof x !== "number" || !Number.isFinite(x) || x < 0 && k !== "exit") throw new Error(`measure stats line has bad "${k}": ${JSON.stringify(line)}`);
		return x;
	};
	return { wall_s: num("wall_s"), user_s: num("user_s"), sys_s: num("sys_s"), max_rss_kb: num("max_rss_kb"), exit: num("exit") };
}

// For an even count the median is the mean of those middle values.
export function median(values: number[]): number {
	if (values.length === 0) throw new Error("median of an empty list");
	const s = [...values].sort((a, b) => a - b);
	const mid = s.length >> 1;
	return s.length % 2 === 1 ? s[mid]! : (s[mid - 1]! + s[mid]!) / 2;
}

export function summarize(runs: RunStats[], logicalBytes: number): Summary {
	if (runs.length === 0) throw new Error("summarize needs at least one run");
	const cpu = runs.map((r) => r.user_s + r.sys_s);
	const wallS = median(runs.map((r) => r.wall_s));
	return {
		cpuS: median(cpu),
		userS: median(runs.map((r) => r.user_s)),
		sysS: median(runs.map((r) => r.sys_s)),
		wallS,
		cpuMinS: Math.min(...cpu),
		cpuMaxS: Math.max(...cpu),
		maxRssKb: median(runs.map((r) => r.max_rss_kb)),
		throughputMiBs: wallS > 0 ? logicalBytes / (1024 * 1024) / wallS : Infinity,
	};
}
