// In-memory Sink and Source over Uint8Array, both seekable.

import { BinpazerError } from "./format.ts";
import type { Sink, Source } from "./format.ts";

export class MemorySink implements Sink {
	// write copies src into its own buffer.
	readonly borrowsChunks = true;
	protected buf: Uint8Array;
	protected pos = 0;
	protected len = 0;

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

// MemoryFile is a growable file that reads, writes and seeks through one position, as resume needs.
export class MemoryFile extends MemorySink implements Source {
	constructor(initial?: Uint8Array) {
		super(Math.max(4096, initial?.length ?? 0));
		if (initial !== undefined) {
			this.write(initial);
			this.pos = 0;
		}
	}

	read(dst: Uint8Array): number {
		const n = Math.min(dst.length, Math.max(0, this.len - this.pos));
		dst.set(this.buf.subarray(this.pos, this.pos + n));
		this.pos += n;
		return n;
	}

	size(): number {
		return this.len;
	}

	// truncate drops every byte from n on.
	truncate(n: number): void {
		this.len = Math.min(this.len, Math.max(0, n));
	}

	// view returns a Source with its own position over the live bytes, as a second process would see them.
	view(): Source {
		let at = 0;
		return {
			read: (dst) => {
				const n = Math.min(dst.length, Math.max(0, this.len - at));
				dst.set(this.buf.subarray(at, at + n));
				at += n;
				return n;
			},
			seek: (to) => {
				at = to;
			},
			size: () => this.len,
		};
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
