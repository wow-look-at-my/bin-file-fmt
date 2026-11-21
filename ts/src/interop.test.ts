// Live C <-> TS interop for every registry codec, driven through c/interop.c.

import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before } from "node:test";
import { compressBlock, readPayloadDecompressed } from "./codec.ts";
import { CODEC_DEFLATE, CODEC_LZ4, CODEC_STORED, CODEC_ZSTD, FLAG_HAS_CRC } from "./format.ts";
import { Reader } from "./reader.ts";
import { FileSink, FileSource, REPO_ROOT, assert, guidOf, test } from "./testio.ts";
import { Writer } from "./writer.ts";

const CODECS: [string, number][] = [
	["stored", CODEC_STORED],
	["deflate", CODEC_DEFLATE],
	["zstd", CODEC_ZSTD],
	["lz4", CODEC_LZ4],
];

let dir = "";
let harness = "";
let payload = new Uint8Array(0);

before(() => {
	dir = mkdtempSync(join(tmpdir(), "binpazer-interop-"));
	harness = join(dir, "interop");
	const c = (f: string): string => join(REPO_ROOT, "c", f);
	execFileSync("cc", ["-std=c11", "-O2", c("binpazer.c"), c("binpazer_codec.c"), c("interop.c"), "-o", harness, "-lz", "-lzstd", "-llz4"], { stdio: "pipe" });
	execFileSync(harness, ["payload", join(dir, "payload.bin")], { stdio: "pipe" });
	execFileSync(harness, ["write", dir], { stdio: "pipe" });
	payload = new Uint8Array(readFileSync(join(dir, "payload.bin")));
});

after(() => {
	if (dir !== "") {
		rmSync(dir, { recursive: true, force: true });
	}
});

test("the C harness produced the canonical payload", () => {
	assert.equal(payload.length, 8192);
});

for (const [name, id] of CODECS) {
	test(`TS decodes the C ${name} file`, () => {
		const src = new FileSource(join(dir, `c-${name}.binpazer`));
		try {
			const rd = new Reader(src);
			assert.equal(rd.writerName, "interop");
			const b = rd.next();
			assert.ok(b !== null);
			assert.equal(b.typeId, 1);
			assert.deepEqual(readPayloadDecompressed(rd, b), payload);
		} finally {
			src.close();
		}
	});

	test(`C decodes the TS ${name} file`, () => {
		const path = join(dir, `ts-${name}.binpazer`);
		const sink = new FileSink(path);
		try {
			const w = new Writer(sink, guidOf(0x01), "interop", [{ typeId: 1, guid: guidOf(0x11), name: "Payload" }]);
			compressBlock(w, 1, FLAG_HAS_CRC, id, null, payload);
			w.end();
		} finally {
			sink.close();
		}
		const out = execFileSync(harness, ["read", path], { encoding: "utf8", stdio: "pipe" });
		assert.match(out, /^ok /);
	});
}
