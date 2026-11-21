
// stream.ts holds NodeWritableLike.

import { BinpazerError } from "./format.ts";
import type { Sink } from "./format.ts";

// The part of a Node Writable that NodeStreamSink calls.
export interface NodeWritableLike {
	write(chunk: Uint8Array): boolean;
	once(event: "drain", listener: () => void): unknown;
	on(event: "error", listener: (err: Error) => void): unknown;
}

export interface WebWriterLike {
	write(chunk: Uint8Array): Promise<void>;
	readonly ready: Promise<unknown>;
}

function streamFailed(cause: unknown): BinpazerError {
	return new BinpazerError("io", "the output stream failed", { cause });
}

// NodeStreamSink writes to a Node stream: a socket, an HTTP response, process.stdout, a file stream.
// It has no seek, so the file keeps the streaming sentinel. The stream queues what it cannot send yet:
// await drained() between blocks to hold memory flat.
export class NodeStreamSink implements Sink {
	readonly keepsChunks = true;
	private readonly stream: NodeWritableLike;
	private full = false;
	private failure: BinpazerError | null = null;
	private waiters: { resolve: () => void; reject: (e: BinpazerError) => void }[] = [];

	constructor(stream: NodeWritableLike) {
		this.stream = stream;
		stream.on("error", (err) => {
			this.failure = streamFailed(err);
			for (const w of this.waiters.splice(0)) {
				w.reject(this.failure);
			}
		});
	}

	write = (src: Uint8Array): void => {
		if (this.failure !== null) {
			throw this.failure;
		}
		if (!this.stream.write(src)) {
			this.full = true;
		}
	};

	// needsDrain is true while the stream holds more than its high-water mark.
	get needsDrain(): boolean {
		return this.full;
	}

	// drained resolves when the stream can take more, and rejects when the stream fails.
	drained(): Promise<void> {
		if (this.failure !== null) {
			return Promise.reject(this.failure);
		}
		if (!this.full) {
			return Promise.resolve();
		}
		return new Promise<void>((resolve, reject) => {
			const first = this.waiters.length === 0;
			this.waiters.push({ resolve, reject });
			if (first) {
				this.stream.once("drain", () => {
					this.full = false;
					for (const w of this.waiters.splice(0)) {
						w.resolve();
					}
				});
			}
		});
	}
}

// The same rules as NodeStreamSink hold: no seek, and await drained() between blocks.
export class WebStreamSink implements Sink {
	readonly keepsChunks = true;
	private readonly writer: WebWriterLike;
	private failure: BinpazerError | null = null;
	private last: Promise<void> = Promise.resolve();

	constructor(writer: WebWriterLike) {
		this.writer = writer;
	}

	write = (src: Uint8Array): void => {
		if (this.failure !== null) {
			throw this.failure;
		}
		this.last = this.writer.write(src).catch((err: unknown) => {
			this.failure = streamFailed(err);
		});
	};

	// drained resolves when the stream wants more data, and rejects when a write failed.
	async drained(): Promise<void> {
		await this.writer.ready.catch((err: unknown) => {
			this.failure ??= streamFailed(err);
		});
		if (this.failure !== null) {
			throw this.failure;
		}
	}

	// finished resolves when every write so far reached the stream, and rejects when one failed.
	async finished(): Promise<void> {
		await this.last;
		if (this.failure !== null) {
			throw this.failure;
		}
	}
}
