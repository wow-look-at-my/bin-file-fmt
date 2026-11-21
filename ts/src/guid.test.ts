// GUID text form: canonical format, permissive hyphens, and rejection of bad input.

import { BLOCK_INDEX_GUID, BLOCK_TYPE_TABLE_GUID, GROUP_GUID, PADDING_GUID, WRITER_INFO_GUID, formatGuid, guidEquals, parseGuid } from "./format.ts";
import { SAMPLE_MESH, assert, codeOf, test } from "./testio.ts";

test("formatGuid writes lowercase canonical text and parseGuid inverts it", () => {
	const text = "11223344-5566-7788-99aa-bbccddeeff00";
	assert.equal(formatGuid(SAMPLE_MESH), text);
	assert.deepEqual(parseGuid(text), SAMPLE_MESH);
	assert.deepEqual(parseGuid(text.toUpperCase()), SAMPLE_MESH);
	assert.deepEqual(parseGuid(text.replaceAll("-", "")), SAMPLE_MESH);
	assert.deepEqual(parseGuid("1122-3344556677-8899aabbccddeeff00"), SAMPLE_MESH);
});

test("the well-known GUIDs spell binpazer and count up from one", () => {
	const known = [BLOCK_TYPE_TABLE_GUID, BLOCK_INDEX_GUID, WRITER_INFO_GUID, PADDING_GUID, GROUP_GUID];
	known.forEach((g, i) => {
		assert.equal(formatGuid(g), `62696e70-617a-6572-0000-00000000000${i + 1}`);
	});
	assert.ok(guidEquals(parseGuid("62696e70-617a-6572-0000-000000000005"), GROUP_GUID));
	assert.ok(!guidEquals(BLOCK_INDEX_GUID, GROUP_GUID));
});

test("parseGuid rejects the wrong length and non-hex digits", () => {
	for (const bad of ["", "1234", "11223344-5566-7788-99aa-bbccddeeff0", "11223344-5566-7788-99aa-bbccddeeff000", "g1223344-5566-7788-99aa-bbccddeeff00", "11223344 5566-7788-99aa-bbccddeeff0"]) {
		assert.equal(codeOf(() => parseGuid(bad)), "format", bad);
	}
	assert.equal(codeOf(() => formatGuid(new Uint8Array(15))), "range");
});
