// Tests for the bench program result-line parser and validator.

import assert from "node:assert/strict";
import { test } from "node:test";
import { formatResultLine, parseResultLine, validateResultLine } from "./resultline.ts";

test("parses one line and canonicalizes it", () => {
	const r = parseResultLine('{"checksum":"DEADBEEF","bytes":512,"blocks":2,"op":"read"}\n');
	assert.deepEqual(r, { op: "read", blocks: 2, bytes: 512, checksum: "deadbeef" });
	assert.equal(formatResultLine(r), '{"op":"read","blocks":2,"bytes":512,"checksum":"deadbeef"}');
});

test("rejects malformed output", () => {
	assert.throws(() => parseResultLine(""), /exactly one stdout line, got 0/);
	assert.throws(() => parseResultLine('{"op":"read"}\n{"op":"read"}\n'), /got 2/);
	assert.throws(() => parseResultLine("hello\n"), /unparsable/);
	assert.throws(() => parseResultLine('{"op":"read","blocks":2,"bytes":512}'), /keys/);
	assert.throws(() => parseResultLine('{"op":"read","blocks":2,"bytes":512,"checksum":"0","x":1}'), /keys/);
	assert.throws(() => parseResultLine('{"op":"scan","blocks":2,"bytes":512,"checksum":"00000000"}'), /bad op/);
	assert.throws(() => parseResultLine('{"op":"read","blocks":2.5,"bytes":512,"checksum":"00000000"}'), /bad blocks/);
	assert.throws(() => parseResultLine('{"op":"read","blocks":2,"bytes":"512","checksum":"00000000"}'), /bad bytes/);
	assert.throws(() => parseResultLine('{"op":"read","blocks":2,"bytes":512,"checksum":"xyz"}'), /8 hex/);
});

test("validates op, blocks, bytes and the write checksum", () => {
	const ok = { op: "read" as const, blocks: 4, bytes: 1024, checksum: "12345678" };
	validateResultLine(ok, "read", 4, 256);
	validateResultLine({ ...ok, op: "write", checksum: "00000000" }, "write", 4, 256);
	assert.throws(() => validateResultLine(ok, "stream", 4, 256), /op is "read", want "stream"/);
	assert.throws(() => validateResultLine(ok, "read", 5, 256), /blocks is 4, want 5/);
	assert.throws(() => validateResultLine(ok, "read", 4, 100), /bytes is 1024, want 400/);
	assert.throws(() => validateResultLine({ ...ok, op: "write" }, "write", 4, 256), /write checksum/);
});
