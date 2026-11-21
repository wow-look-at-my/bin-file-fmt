// The TS writer reproduces the C fixtures byte for byte, and the TS reader reads them back.

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { readPayloadDecompressed } from "./codec.ts";
import { parseEnvelope } from "./envelope.ts";
import { BLOCK_TYPE_TABLE_GUID, CODEC_BY_GUID, CODEC_STORED, FLAG_COMPRESSED, FLAG_CRITICAL, FLAG_HAS_CRC, TYPE_BLOCK_TYPE_TABLE } from "./format.ts";
import type { Source } from "./format.ts";
import { MemorySink, MemorySource } from "./memory.ts";
import { Reader } from "./reader.ts";
import { BIG_PAYLOAD, MESH_PAYLOAD, NOTE_PAYLOAD, SAMPLE_CODEC, SAMPLE_MESH, SAMPLE_NOTE, SAMPLE_WRITER, TESTDATA, assert, enc, streamSource, test } from "./testio.ts";
import { writeCompressedSample, writeSample } from "./testio.ts";

const sample = new Uint8Array(readFileSync(join(TESTDATA, "sample.binpazer")));
const sampleCompressed = new Uint8Array(readFileSync(join(TESTDATA, "sample-compressed.binpazer")));

test("writer reproduces sample.binpazer byte for byte", () => {
	const m = new MemorySink();
	writeSample(m);
	assert.equal(sample.length, 280);
	assert.deepEqual(m.bytes(), sample);
});

test("writer reproduces sample-compressed.binpazer byte for byte", () => {
	const m = new MemorySink();
	writeCompressedSample(m);
	assert.deepEqual(m.bytes(), sampleCompressed);
});

function verifySample(src: Source): void {
	const r = new Reader(src);
	assert.equal(r.versionMajor, 1);
	assert.equal(r.versionMinor, 0);
	assert.equal(r.writerName, "demo");
	assert.deepEqual(r.writerGuid, SAMPLE_WRITER);
	assert.equal(r.fileLength, 280);
	assert.equal(r.firstBlockOffset, 72);
	assert.equal(r.hasIndex, false);
	assert.deepEqual(r.types, [
		{ typeId: TYPE_BLOCK_TYPE_TABLE, guid: BLOCK_TYPE_TABLE_GUID, name: "BlockTypeTable" },
		{ typeId: 1, guid: SAMPLE_MESH, name: "Mesh" },
		{ typeId: 2, guid: SAMPLE_NOTE, name: "Note" },
	]);
	const want = [
		{ typeId: 1, flags: FLAG_CRITICAL | FLAG_HAS_CRC, guid: SAMPLE_MESH, name: "Mesh", payload: MESH_PAYLOAD, offset: 192 },
		{ typeId: 2, flags: 0, guid: SAMPLE_NOTE, name: "Note", payload: NOTE_PAYLOAD, offset: 224 },
		{ typeId: 1, flags: 0, guid: SAMPLE_MESH, name: "Mesh", payload: BIG_PAYLOAD, offset: 248 },
	];
	for (const w of want) {
		const b = r.next();
		assert.ok(b !== null);
		assert.equal(b.typeId, w.typeId);
		assert.equal(b.flags, w.flags);
		assert.equal(b.known, true);
		assert.deepEqual(b.guid, w.guid);
		assert.equal(b.name, w.name);
		assert.equal(b.offset, w.offset);
		assert.equal(b.payloadOffset, w.offset + 8);
		assert.equal(b.payloadLength, w.payload.length);
		assert.deepEqual(r.readPayload(b), enc(w.payload));
	}
	assert.equal(r.next(), null);
}

test("reader reads sample.binpazer, seekable and streaming", () => {
	verifySample(new MemorySource(sample));
	verifySample(streamSource(new MemorySource(sample)));
});

test("reader reads sample-compressed.binpazer", () => {
	const r = new Reader(new MemorySource(sampleCompressed));
	assert.equal(r.versionMinor, 1);
	assert.equal(r.fileLength, sampleCompressed.length);
	assert.equal(r.types.length, 2);

	const plain = r.next();
	assert.ok(plain !== null);
	assert.equal(plain.flags, 0);
	assert.deepEqual(readPayloadDecompressed(r, plain), enc("plain block"));

	const stored = r.next();
	assert.ok(stored !== null);
	assert.equal(stored.flags, FLAG_HAS_CRC | FLAG_COMPRESSED);
	const raw = r.readPayload(stored);
	const env = parseEnvelope(raw);
	assert.equal(env.codecId, CODEC_STORED);
	assert.equal(env.uncompressedLength, 19);
	assert.equal(env.storedLength, 19);
	assert.deepEqual(raw.subarray(env.headerSize), enc("compressed, sort of"));

	const byGuid = r.next();
	assert.ok(byGuid !== null);
	assert.equal(byGuid.flags, FLAG_COMPRESSED);
	const raw2 = r.readPayload(byGuid);
	const env2 = parseEnvelope(raw2);
	assert.equal(env2.codecId, CODEC_BY_GUID);
	assert.deepEqual(env2.codecGuid, SAMPLE_CODEC);
	assert.equal(env2.headerSize, 32);
	assert.deepEqual(raw2.subarray(32), enc("guid-codec payload"));
	assert.equal(r.next(), null);
});
