// Streaming writes: sinks that keep their chunks, real Node and Web streams, backpressure and stream failure.

import { createWriteStream, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join as joinPath } from "node:path";
import { PassThrough, Writable } from "node:stream";
import { compressBlock, readPayloadDecompressed } from "./codec.ts";
import { CODEC_ZSTD, FLAG_HAS_CRC, VERSION_MINOR_COMPRESSION } from "./format.ts";
import type { Sink } from "./format.ts";
import * as mem from "./memory.ts";
import { Reader } from "./reader.ts";
import { NodeStreamSink, WebStreamSink } from "./stream.ts";
import { SAMPLE_NOTE, SAMPLE_WRITER, assert, codeOf, pattern, streamSource, test } from "./testio.ts";
import { Writer } from "./writer.ts";

const BIG = 200_000;
const BLOCKS = 600;

interface Shape {
	appendable?: boolean;
	// The caller fills one scratch array again for every block, as a real producer does.
	reuse?: boolean;
}

// payloadOf gives block i its bytes: every seventh block is larger than the writer's buffer.
function payloadOf(i: number, scratch: Uint8Array | null): Uint8Array {
	const n = i % 7 === 0 ? BIG : 100 + (i % 50);
	const p = scratch === null ? new Uint8Array(n) : scratch.subarray(0, n);
	p.fill(i & 0xff);
	return p;
}

function open(sink: Sink, shape: Shape): Writer {
	return new Writer(sink, SAMPLE_WRITER, "stream", [{ typeId: 2, guid: SAMPLE_NOTE, name: "Note" }], { appendable: shape.appendable });
}

// produce writes the blocks; between is the caller's chance to wait for the stream.
async function produce(sink: Sink, shape: Shape, between: () => Promise<void>): Promise<void> {
	const w = open(sink, shape);
	const scratch = shape.reuse === true ? new Uint8Array(BIG) : null;
	for (let i = 0; i < BLOCKS; i++) {
		w.block(2, FLAG_HAS_CRC, payloadOf(i, scratch));
		await between();
	}
	w.end();
}

const noWait = (): Promise<void> => Promise.resolve();

// expected is what a sink that copies every chunk at once receives, with no seek.
async function expected(shape: Shape): Promise<Uint8Array> {
	const copy = new mem.MemorySink();
	await produce({ write: (b) => copy.write(b) }, { ...shape, reuse: false }, noWait);
	return copy.bytes();
}

function concat(chunks: Uint8Array[]): Uint8Array {
	const out = new Uint8Array(chunks.reduce((n, c) => n + c.length, 0));
	let at = 0;
	for (const c of chunks) {
		out.set(c, at);
		at += c.length;
	}
	return out;
}

const tick = (ms = 0): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

for (const appendable of [false, true]) {
	test(`a sink that keeps every chunk gets the right bytes while the caller reuses its payload array (appendable ${appendable})`, async () => {
		const kept: Uint8Array[] = [];
		await produce({ write: (b) => void kept.push(b) }, { appendable, reuse: true }, noWait);
		assert.deepEqual(concat(kept), await expected({ appendable }));
	});

	test(`a slow Node stream gets the right bytes while the caller reuses its payload array (appendable ${appendable})`, async () => {
		const stream = new PassThrough({ highWaterMark: 16 * 1024 });
		const got: Uint8Array[] = [];
		stream.on("data", (c: Uint8Array) => got.push(new Uint8Array(c)));
		stream.pause();
		setTimeout(() => stream.resume(), 5);
		const sink = new NodeStreamSink(stream);
		let waits = 0;
		await produce(sink, { appendable, reuse: true }, async () => {
			if (sink.needsDrain) {
				waits++;
				await sink.drained();
			}
		});
		stream.end();
		await new Promise<void>((resolve) => stream.on("end", () => resolve()));
		assert.deepEqual(concat(got), await expected({ appendable }));
		assert.ok(waits > 0, "the stream pushed back at least once");
	});
}

test("only a sink that declares borrowsChunks receives the caller's own payload array", () => {
	const payload = pattern(BIG, 9);
	const seen = (borrows: boolean): Uint8Array[] => {
		const chunks: Uint8Array[] = [];
		const w = open({ write: (b) => void chunks.push(b), borrowsChunks: borrows }, {});
		w.block(2, 0, payload);
		w.end();
		return chunks;
	};
	assert.ok(seen(true).some((c) => c.buffer === payload.buffer), "a borrowing sink gets the payload with no copy");
	assert.ok(!seen(false).some((c) => c.buffer === payload.buffer), "any other sink gets a copy");
});

test("a streaming file differs from the seekable file only in file_length", async () => {
	const build = (sink: Sink): void => {
		const w = new Writer(sink, SAMPLE_WRITER, "stream", [{ typeId: 2, guid: SAMPLE_NOTE, name: "Note" }], { versionMinor: VERSION_MINOR_COMPRESSION });
		w.block(2, FLAG_HAS_CRC, pattern(300, 3));
		compressBlock(w, 2, FLAG_HAS_CRC, CODEC_ZSTD, null, pattern(BIG, 4));
		w.end();
	};
	const seekable = new mem.MemorySink();
	build(seekable);
	const pipe = new mem.MemorySink();
	build({ write: (b) => pipe.write(b) });

	const want = seekable.bytes().slice();
	want.fill(0xff, 32, 40);
	assert.deepEqual(pipe.bytes(), want);

	const rd = new Reader(streamSource(new mem.MemorySource(pipe.bytes())));
	assert.equal(rd.fileLength, null);
	const first = rd.next();
	assert.ok(first !== null);
	assert.deepEqual(rd.readPayload(first), pattern(300, 3));
	const second = rd.next();
	assert.ok(second !== null);
	assert.deepEqual(readPayloadDecompressed(rd, second), pattern(BIG, 4));
	assert.equal(rd.next(), null);
});

test("drained waits until the Node stream takes more", async () => {
	const stream = new PassThrough({ highWaterMark: 1024 });
	stream.pause();
	stream.on("data", () => undefined);
	stream.pause();
	const sink = new NodeStreamSink(stream);
	assert.equal(sink.needsDrain, false);
	await sink.drained();

	sink.write(new Uint8Array(4096));
	assert.equal(sink.needsDrain, true);
	let done = 0;
	const a = sink.drained().then(() => done++);
	const b = sink.drained().then(() => done++);
	await tick(10);
	assert.equal(done, 0, "nothing resolves while the stream is full");
	stream.resume();
	await Promise.all([a, b]);
	assert.equal(done, 2);
	assert.equal(sink.needsDrain, false);
});

test("a failed Node stream makes the next block throw and drained reject", async () => {
	const stream = new Writable({
		highWaterMark: 16,
		write(_chunk, _enc, cb) {
			cb(new Error("disk gone"));
		},
	});
	const sink = new NodeStreamSink(stream);
	const w = open(sink, { appendable: true });
	w.block(2, 0, pattern(64));
	const waiting = sink.drained();
	await assert.rejects(waiting, /output stream failed/);
	assert.equal(codeOf(() => w.block(2, 0, pattern(64))), "io");
	assert.equal(codeOf(() => w.end()), "io");
	await assert.rejects(sink.drained(), /output stream failed/);
});

test("a Node file stream receives the file", async () => {
	const dir = mkdtempSync(joinPath(tmpdir(), "binpazer-stream-"));
	try {
		const path = joinPath(dir, "out.binpazer");
		const file = createWriteStream(path, { highWaterMark: 32 * 1024 });
		const sink = new NodeStreamSink(file);
		await produce(sink, { reuse: true }, () => sink.drained());
		await new Promise<void>((resolve, reject) => {
			file.on("error", reject);
			file.end(() => resolve());
		});
		assert.deepEqual(new Uint8Array(readFileSync(path)), await expected({}));
	} finally {
		rmSync(dir, { recursive: true, force: true });
	}
});

test("a Web stream gets the right bytes with backpressure", async () => {
	const got: Uint8Array[] = [];
	const web = new WritableStream<Uint8Array>(
		{
			async write(chunk) {
				await tick();
				got.push(new Uint8Array(chunk));
			},
		},
		new ByteLengthQueuingStrategy({ highWaterMark: 16 * 1024 }),
	);
	const writer = web.getWriter();
	const sink = new WebStreamSink(writer);
	await produce(sink, { reuse: true }, () => sink.drained());
	await sink.finished();
	await writer.close();
	assert.deepEqual(concat(got), await expected({}));
});

test("a failed Web stream makes the next block throw", async () => {
	const web = new WritableStream<Uint8Array>({
		write() {
			throw new Error("connection reset");
		},
	});
	const sink = new WebStreamSink(web.getWriter());
	const w = open(sink, { appendable: true });
	w.block(2, 0, pattern(64));
	await assert.rejects(sink.finished(), /output stream failed/);
	await assert.rejects(sink.drained(), /output stream failed/);
	assert.equal(codeOf(() => w.block(2, 0, pattern(64))), "io");
});
