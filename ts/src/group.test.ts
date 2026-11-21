// parseGroup: aligned children, CRC results, and the intact prefix of a damaged group.

import { FLAG_HAS_CRC, blockHeaderBytes, crc32c, putU32 } from "./format.ts";
import { parseGroup } from "./group.ts";
import { assert, enc, test } from "./testio.ts";

// rawChild builds one on-disk child: header, payload, optional CRC, zero padding.
function rawChild(typeId: number, flags: number, payload: Uint8Array): number[] {
	const head = blockHeaderBytes(typeId, flags, payload.length);
	const out = [...head, ...payload];
	if (flags & FLAG_HAS_CRC) {
		const cb = new Uint8Array(4);
		putU32(cb, 0, crc32c(crc32c(0, head), payload));
		out.push(...cb);
	}
	while (out.length % 8 !== 0) {
		out.push(0);
	}
	return out;
}

test("parseGroup walks aligned children and verifies their CRCs", () => {
	const gp = Uint8Array.from([...rawChild(1, FLAG_HAS_CRC, enc("mesh-data")), ...rawChild(2, 0, enc("note"))]);
	const { children, error } = parseGroup(gp);
	assert.equal(error, null);
	assert.equal(children.length, 2);
	assert.equal(children[0].typeId, 1);
	assert.equal(children[0].flags, FLAG_HAS_CRC);
	assert.equal(children[0].offset, 0);
	assert.deepEqual(children[0].payload, enc("mesh-data"));
	assert.equal(children[0].payloadLength, 9);
	assert.equal(children[0].crcOk, true);
	assert.equal(children[0].payload.buffer, gp.buffer, "a child payload is a view, not a copy");
	assert.equal(children[1].typeId, 2);
	assert.deepEqual(children[1].payload, enc("note"));
	assert.equal(children[1].crcOk, true);
	assert.equal(children[1].offset % 8, 0);

	const none = parseGroup(new Uint8Array(0));
	assert.equal(none.error, null);
	assert.equal(none.children.length, 0);
});

test("a CRC mismatch is recorded, not fatal", () => {
	const gp = Uint8Array.from(rawChild(1, FLAG_HAS_CRC, enc("mesh-data")));
	gp[8] ^= 0xff;
	const { children, error } = parseGroup(gp);
	assert.equal(error, null);
	assert.equal(children.length, 1);
	assert.equal(children[0].crcOk, false);
});

test("a malformed group returns the intact prefix with the error", () => {
	const gp = Uint8Array.from([...rawChild(1, 0, enc("ok")), ...rawChild(2, 0, enc("victim"))]);
	putU32(gp, 16 + 4, 0xffff);
	const bad = parseGroup(gp);
	assert.equal(bad.error?.code, "truncated");
	assert.equal(bad.children.length, 1);
	assert.equal(bad.children[0].typeId, 1);

	assert.equal(parseGroup(enc("junk!")).error?.code, "truncated");

	const padded = parseGroup(Uint8Array.from([...rawChild(1, 0, new Uint8Array(0)), 0, 0, 0, 0]));
	assert.equal(padded.error, null);
	assert.equal(padded.children.length, 1);
});

test("a final child whose padding is not materialized is tolerated", () => {
	const gp = Uint8Array.from(rawChild(3, 0, enc("abc"))).subarray(0, 11);
	const { children, error } = parseGroup(gp);
	assert.equal(error, null);
	assert.equal(children.length, 1);
	assert.deepEqual(children[0].payload, enc("abc"));
});
