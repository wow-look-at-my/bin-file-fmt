// Minimal decoder for gzip-compressed pprof CPU profiles: self time per leaf function.

import { gunzipSync } from "node:zlib";

interface Field {
	num: number;
	wire: number;
	int: number;
	bytes: Buffer | null;
}

class Reader {
	readonly buf: Buffer;
	pos = 0;

	constructor(buf: Buffer) {
		this.buf = buf;
	}

	varint(): number {
		let result = 0;
		let scale = 1;
		for (;;) {
			if (this.pos >= this.buf.length) throw new Error("pprof: truncated varint");
			const b = this.buf[this.pos++]!;
			result += (b & 0x7f) * scale;
			if (b < 0x80) break;
			scale *= 128;
			if (scale > 2 ** 63) throw new Error("pprof: varint longer than 10 bytes");
		}
		return result;
	}

	take(n: number): Buffer {
		if (this.pos + n > this.buf.length) throw new Error("pprof: truncated field");
		const out = this.buf.subarray(this.pos, this.pos + n);
		this.pos += n;
		return out;
	}
}

// Splits one protobuf message into its fields; group wire types are an error.
export function decodeFields(buf: Buffer): Field[] {
	const r = new Reader(buf);
	const out: Field[] = [];
	while (r.pos < buf.length) {
		const tag = r.varint();
		const num = Math.floor(tag / 8);
		const wire = tag % 8;
		if (wire === 0) out.push({ num, wire, int: r.varint(), bytes: null });
		else if (wire === 1) out.push({ num, wire, int: 0, bytes: r.take(8) });
		else if (wire === 2) out.push({ num, wire, int: 0, bytes: r.take(r.varint()) });
		else if (wire === 5) out.push({ num, wire, int: 0, bytes: r.take(4) });
		else throw new Error(`pprof: unsupported wire type ${wire} for field ${num}`);
	}
	return out;
}

// Repeated integer field: plain varints and packed runs are both legal.
function ints(fields: Field[], num: number): number[] {
	const out: number[] = [];
	for (const f of fields) {
		if (f.num !== num) continue;
		if (f.wire === 0) out.push(f.int);
		else if (f.wire === 2) {
			const r = new Reader(f.bytes!);
			while (r.pos < f.bytes!.length) out.push(r.varint());
		} else throw new Error(`pprof: field ${num} has wire type ${f.wire}, want an integer`);
	}
	for (const v of out) if (!Number.isSafeInteger(v)) throw new Error(`pprof: field ${num} holds ${v}, above 2^53`);
	return out;
}

function int(fields: Field[], num: number): number {
	const v = ints(fields, num);
	return v.length === 0 ? 0 : v[v.length - 1]!;
}

function messages(fields: Field[], num: number): Field[][] {
	return fields.filter((f) => f.num === num).map((f) => {
		if (f.wire !== 2) throw new Error(`pprof: field ${num} is not a message`);
		return decodeFields(f.bytes!);
	});
}

// Returns nanoseconds of cpu self time per function name, keyed by the leaf location's innermost function.
export function pprofSelfTimes(gz: Buffer): Map<string, number> {
	const top = decodeFields(gunzipSync(gz));
	const strings = top.filter((f) => f.num === 6).map((f) => {
		if (f.wire !== 2) throw new Error("pprof: string_table entry is not a string");
		return f.bytes!.toString("utf8");
	});
	const str = (i: number): string => {
		const s = strings[i];
		if (s === undefined) throw new Error(`pprof: string index ${i} is out of range`);
		return s;
	};
	const types = messages(top, 1).map((m) => `${str(int(m, 1))}/${str(int(m, 2))}`);
	const cpuIndex = types.indexOf("cpu/nanoseconds");
	if (cpuIndex < 0) throw new Error(`pprof: no cpu/nanoseconds sample type (have ${types.join(", ") || "none"})`);
	const functions = new Map<number, string>();
	for (const m of messages(top, 5)) functions.set(int(m, 1), str(int(m, 2)));
	const locations = new Map<number, string>();
	for (const m of messages(top, 4)) {
		const lines = messages(m, 4);
		let name: string;
		if (lines.length === 0) name = `0x${int(m, 3).toString(16)}`;
		else {
			const fid = int(lines[0]!, 1);
			const fn = functions.get(fid);
			if (fn === undefined) throw new Error(`pprof: location ${int(m, 1)} names unknown function ${fid}`);
			name = fn;
		}
		locations.set(int(m, 1), name);
	}
	const self = new Map<string, number>();
	for (const s of messages(top, 2)) {
		const locs = ints(s, 1);
		const values = ints(s, 2);
		if (values.length !== types.length) throw new Error(`pprof: sample has ${values.length} values for ${types.length} sample types`);
		if (locs.length === 0) throw new Error("pprof: sample has no location");
		const name = locations.get(locs[0]!);
		if (name === undefined) throw new Error(`pprof: sample names unknown location ${locs[0]}`);
		self.set(name, (self.get(name) ?? 0) + values[cpuIndex]!);
	}
	return self;
}
