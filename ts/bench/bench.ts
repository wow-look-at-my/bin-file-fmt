// bench.ts is the TypeScript bench program: fixed workloads whose one-line output the cross-language driver compares.
import * as fs from "node:fs";
import {
	BinpazerError,
	CODEC_DEFLATE,
	CODEC_LZ4,
	CODEC_STORED,
	CODEC_ZSTD,
	FLAG_HAS_CRC,
	Reader,
	Writer,
	compressBlock,
	readPayloadDecompressed,
} from "../src/index.ts";
import type { Block, Sink, Source } from "../src/index.ts";

const TYPE_PAYLOAD = 1;
const STREAM_CHUNK = 65536;

const CODECS: Record<string, number> = {
	none: 0,
	stored: CODEC_STORED,
	deflate: CODEC_DEFLATE,
	zstd: CODEC_ZSTD,
	lz4: CODEC_LZ4,
};

class FileSource implements Source {
	private readonly fd: number;
	private pos = 0;

	constructor(path: string) {
		this.fd = fs.openSync(path, "r");
	}

	read(dst: Uint8Array): number {
		let got = 0;
		while (got < dst.length) {
			const n = fs.readSync(this.fd, dst, got, dst.length - got, this.pos);
			if (n === 0) break;
			got += n;
			this.pos += n;
		}
		return got;
	}

	seek(offset: number): void {
		this.pos = offset;
	}

	size(): number {
		return fs.fstatSync(this.fd).size;
	}

	close(): void {
		fs.closeSync(this.fd);
	}
}

class FileSink implements Sink {
	// writeSync is done with the bytes when it returns, so large payloads go out with no copy.
	readonly borrowsChunks = true;
	private readonly fd: number;
	private pos = 0;

	constructor(path: string) {
		this.fd = fs.openSync(path, "w");
	}

	write(src: Uint8Array): void {
		let done = 0;
		while (done < src.length) {
			const n = fs.writeSync(this.fd, src, done, src.length - done, this.pos);
			done += n;
			this.pos += n;
		}
	}

	seek(offset: number): void {
		this.pos = offset;
	}

	close(): void {
		fs.closeSync(this.fd);
	}
}

// Sum is the FNV-1a checksum over the length and sample bytes of each block.
class Sum {
	h = 2166136261;

	mix(b: number): void {
		this.h = Math.imul(this.h ^ b, 16777619) >>> 0;
	}

	block(length: number, first: number, mid: number, last: number): void {
		const n = length % 4294967296;
		this.mix(n & 0xff);
		this.mix((n >>> 8) & 0xff);
		this.mix((n >>> 16) & 0xff);
		this.mix((n >>> 24) & 0xff);
		if (length > 0) {
			this.mix(first);
			this.mix(mid);
			this.mix(last);
		}
	}

	payload(p: Uint8Array): void {
		const n = p.length;
		if (n === 0) this.block(0, 0, 0, 0);
		else this.block(n, p[0], p[n >> 1], p[n - 1]);
	}
}

interface Result {
	op: string;
	blocks: number;
	bytes: number;
	sum: Sum | null;
}

function count(text: string, what: string): number {
	if (!/^[0-9]+$/.test(text)) throw new Error(`bench: ${what} must be a non-negative integer, got "${text}"`);
	return Number(text);
}

function guid(first: number): Uint8Array {
	const g = new Uint8Array(16);
	g[0] = first;
	return g;
}

function write(out: string, payloadPath: string, blocksArg: string, codec: string): Result {
	const blocks = count(blocksArg, "block count");
	if (!Object.hasOwn(CODECS, codec)) {
		throw new Error(`bench: unknown codec "${codec}" (want none, stored, deflate, zstd or lz4)`);
	}
	const codecId = CODECS[codec];
	const payload = new Uint8Array(fs.readFileSync(payloadPath));
	const sink = new FileSink(out);
	try {
		const w = new Writer(sink, guid(0xb0), "bench", [{ typeId: TYPE_PAYLOAD, guid: guid(0x11), name: "Payload" }]);
		for (let i = 0; i < blocks; i++) {
			if (codecId === 0) w.block(TYPE_PAYLOAD, FLAG_HAS_CRC, payload);
			else compressBlock(w, TYPE_PAYLOAD, FLAG_HAS_CRC, codecId, null, payload);
		}
		w.writeIndex();
		w.end();
	} finally {
		sink.close();
	}
	return { op: "write", blocks, bytes: blocks * payload.length, sum: null };
}

// walk calls fn for each type-1 block and skips every other block.
function walk(path: string, fn: (r: Reader, b: Block) => void): void {
	const source = new FileSource(path);
	try {
		const r = new Reader(source);
		for (let b = r.next(); b !== null; b = r.next()) {
			if (b.typeId === TYPE_PAYLOAD) fn(r, b);
			else r.skipPayload(b);
		}
	} finally {
		source.close();
	}
}

function read(path: string): Result {
	const res: Result = { op: "read", blocks: 0, bytes: 0, sum: new Sum() };
	walk(path, (r, b) => {
		const p = readPayloadDecompressed(r, b);
		res.blocks++;
		res.bytes += p.length;
		res.sum?.payload(p);
	});
	return res;
}

function stream(path: string): Result {
	const res: Result = { op: "stream", blocks: 0, bytes: 0, sum: new Sum() };
	const scratch = new Uint8Array(STREAM_CHUNK);
	walk(path, (r, b) => {
		const mid = b.payloadLength >> 1;
		let n = 0;
		let first = 0;
		let at = 0;
		let last = 0;
		r.streamPayload(
			b,
			(chunk) => {
				if (chunk.length === 0) return;
				if (n === 0) first = chunk[0];
				if (mid >= n && mid - n < chunk.length) at = chunk[mid - n];
				last = chunk[chunk.length - 1];
				n += chunk.length;
			},
			scratch,
		);
		res.blocks++;
		res.bytes += n;
		res.sum?.block(n, first, at, last);
	});
	return res;
}

function find(path: string, lookupsArg: string): Result {
	const lookups = count(lookupsArg, "lookup count");
	const res: Result = { op: "find", blocks: lookups, bytes: 0, sum: new Sum() };
	const source = new FileSource(path);
	try {
		const r = new Reader(source);
		const offsets = r.find(TYPE_PAYLOAD);
		if (offsets.length === 0) throw new Error("bench: the input has no type-1 blocks");
		for (let k = 0; k < lookups; k++) {
			const idx = (Math.imul(k, 2654435761 | 0) >>> 0) % offsets.length;
			const p = readPayloadDecompressed(r, r.at(offsets[idx]));
			res.bytes += p.length;
			res.sum?.payload(p);
		}
	} finally {
		source.close();
	}
	return res;
}

function run(argv: string[]): Result {
	const [op, ...args] = argv;
	if (op === "write" && args.length === 4) return write(args[0], args[1], args[2], args[3]);
	if (op === "read" && args.length === 1) return read(args[0]);
	if (op === "stream" && args.length === 1) return stream(args[0]);
	if (op === "find" && args.length === 2) return find(args[0], args[1]);
	throw new Error("usage: bench write <out> <payload> <blocks> <codec> | read <in> | stream <in> | find <in> <lookups>");
}

try {
	const res = run(process.argv.slice(2));
	const checksum = (res.sum === null ? 0 : res.sum.h).toString(16).padStart(8, "0");
	process.stdout.write(`{"op":"${res.op}","blocks":${res.blocks},"bytes":${res.bytes},"checksum":"${checksum}"}\n`);
} catch (err) {
	const code = err instanceof BinpazerError ? ` [${err.code}]` : "";
	process.stderr.write(`${err instanceof Error ? err.message : String(err)}${code}\n`);
	process.exitCode = 1;
}
