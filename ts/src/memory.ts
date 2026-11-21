// In-memory Sink and Source over Uint8Array, both seekable.

import { BinpazerError } from "./format.ts";
import type { Sink, Source } from "./format.ts";

export class MemorySink implements Sink {
	private buf: Uint8Array;
	private pos = 0;
	private len = 0;

	constructor(initialCapacity = 4096) {
		this.buf = new Uint8Array(Math.max(16, initialCapacity));
	}

	write(src: Uint8Array): void {
		const end = this.pos + src.length;
		if (end > this.buf.length) {
			let cap = this.buf.length;
			while (cap < end) {
				cap *= 2;
			}
			const next = new Uint8Array(cap);
			next.set(this.buf.subarray(0, this.len));
			this.buf = next;
		}
		if (this.pos > this.len) {
			this.buf.fill(0, this.len, this.pos);
		}
		this.buf.set(src, this.pos);
		this.pos = end;
		if (end > this.len) {
			this.len = end;
		}
	}

	seek(offset: number): void {
		if (!Number.isSafeInteger(offset) || offset < 0) {
			throw new BinpazerError("range", `seek offset ${offset} is invalid`);
		}
		this.pos = offset;
	}

	get length(): number {
		return this.len;
	}

	// bytes returns a copy of everything written.
	bytes(): Uint8Array {
		return this.buf.slice(0, this.len);
	}
}

export class MemorySource implements Source {
	private readonly data: Uint8Array;
	private pos = 0;

	constructor(data: Uint8Array) {
		this.data = data;
	}

	read(dst: Uint8Array): number {
		const n = Math.min(dst.length, Math.max(0, this.data.length - this.pos));
		dst.set(this.data.subarray(this.pos, this.pos + n));
		this.pos += n;
		return n;
	}

	seek(offset: number): void {
		if (!Number.isSafeInteger(offset) || offset < 0) {
			throw new BinpazerError("range", `seek offset ${offset} is invalid`);
		}
		this.pos = offset;
	}

	size(): number {
		return this.data.length;
	}
}
