// Test support: file-descriptor Source and Sink with explicit positions, plus shared fixtures.

import { closeSync, fstatSync, openSync, readSync, writeSync } from "node:fs";
import { join } from "node:path";
import assert from "node:assert/strict";
import { BinpazerError, CODEC_BY_GUID, CODEC_STORED, FLAG_CRITICAL, FLAG_HAS_CRC } from "./format.ts";
import type { Sink, Source } from "./format.ts";
import { MemorySink } from "./memory.ts";
import type { Block, Reader } from "./reader.ts";
import { Writer } from "./writer.ts";

export { default as assert } from "node:assert/strict";
export { test } from "node:test";

// codeOf runs f, requires it to throw a BinpazerError, and returns the error code.
export function codeOf(f: () => unknown): string {
	try {
		f();
	} catch (e) {
		assert.ok(e instanceof BinpazerError, `expected BinpazerError, got ${String(e)}`);
		return e.code;
	}
	assert.fail("expected a BinpazerError");
}

export function mustNext(r: Reader): Block {
	const b = r.next();
	assert.ok(b !== null, "expected another block");
	return b;
}

export function rng(seed: number): (n: number) => number {
	let s = seed | 0 || 1;
	return (n) => {
		s ^= s << 13;
		s ^= s >>> 17;
		s ^= s << 5;
		return (s >>> 0) % n;
	};
}

// pattern returns deterministic pseudo-random bytes.
export function pattern(n: number, seed = 1): Uint8Array {
	const p = new Uint8Array(n);
	let x = seed;
	for (let i = 0; i < n; i++) {
		x = (Math.imul(x, 1103515245) + 12345) >>> 0;
		p[i] = x >>> 24;
	}
	return p;
}

export class FileSource implements Source {
	private readonly fd: number;
	private at = 0;

	constructor(path: string) {
		this.fd = openSync(path, "r");
	}

	read = (dst: Uint8Array): number => {
		let got = 0;
		while (got < dst.length) {
			const n = readSync(this.fd, dst, got, dst.length - got, this.at);
			if (n === 0) {
				break;
			}
			got += n;
			this.at += n;
		}
		return got;
	};

	seek = (to: number): void => {
		this.at = to;
	};

	size = (): number => fstatSync(this.fd).size;

	close(): void {
		closeSync(this.fd);
	}
}

export class FileSink implements Sink {
	readonly borrowsChunks = true;
	private readonly fd: number;
	private at = 0;

	constructor(path: string) {
		this.fd = openSync(path, "w+");
	}

	write = (src: Uint8Array): void => {
		let done = 0;
		while (done < src.length) {
			done += writeSync(this.fd, src, done, src.length - done, this.at + done);
		}
		this.at += src.length;
	};

	seek = (to: number): void => {
		this.at = to;
	};

	close(): void {
		closeSync(this.fd);
	}
}

// streamSource hides seek and size so the reader takes the non-seekable path.
export function streamSource(inner: Source): Source {
	return { read: (dst) => inner.read(dst) };
}

// StreamSink has no seek, like a pipe; it counts the write calls it receives.
export class StreamSink implements Sink {
	readonly mem = new MemorySink();
	writes = 0;

	write = (src: Uint8Array): void => {
		this.mem.write(src);
		this.writes++;
	};
}

export const REPO_ROOT = join(import.meta.dirname, "..", "..");
export const TESTDATA = join(REPO_ROOT, "go", "testdata");

export const enc = (s: string): Uint8Array => new TextEncoder().encode(s);

export function guidOf(...head: number[]): Uint8Array {
	const g = new Uint8Array(16);
	g.set(head);
	return g;
}

export const SAMPLE_WRITER = guidOf(0xaa, 0xbb, 0xcc, 0xdd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1);
export const SAMPLE_MESH = guidOf(0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00);
export const SAMPLE_NOTE = guidOf(0xde, 0xad, 0xbe, 0xef);
export const SAMPLE_CODEC = guidOf(0xc0, 0xde, 0xc0, 0xde, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 7);
export const MESH_PAYLOAD = "vertices-go-here";
export const NOTE_PAYLOAD = "hello, file!";
export const BIG_PAYLOAD = "0123456789ABCDEF01234567";

// writeSample reproduces the main fixture of c/gen.c.
export function writeSample(sink: Sink): void {
	const w = new Writer(sink, SAMPLE_WRITER, "demo", [
		{ typeId: 1, guid: SAMPLE_MESH, name: "Mesh" },
		{ typeId: 2, guid: SAMPLE_NOTE, name: "Note" },
	]);
	w.block(1, FLAG_CRITICAL | FLAG_HAS_CRC, enc(MESH_PAYLOAD));
	w.block(2, 0, enc(NOTE_PAYLOAD));
	w.block(1, 0, enc(BIG_PAYLOAD));
	w.end();
}

// writeCompressedSample reproduces the compressed fixture of c/gen.c.
export function writeCompressedSample(sink: Sink): void {
	const w = new Writer(sink, SAMPLE_WRITER, "demo", [{ typeId: 2, guid: SAMPLE_NOTE, name: "Note" }]);
	w.block(2, 0, enc("plain block"));
	w.blockCompressed(2, FLAG_HAS_CRC, CODEC_STORED, null, enc("compressed, sort of"), 19);
	w.blockCompressed(2, 0, CODEC_BY_GUID, SAMPLE_CODEC, enc("guid-codec payload"), 18);
	w.end();
}

export function sampleBytes(): Uint8Array {
	const m = new MemorySink();
	writeSample(m);
	return m.bytes();
}
