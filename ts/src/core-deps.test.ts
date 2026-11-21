// The core modules import nothing from node: and nothing from the codec layer, so they run in a browser.

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { assert, test } from "./testio.ts";

const CORE = ["format.ts", "writer.ts", "reader.ts", "group.ts", "envelope.ts", "memory.ts", "core.ts"];
const FORBIDDEN = /(?:from|import)\s*\(?\s*["'](?:node:[^"']*|\.\/codec\.ts|\.\/lz4\.ts)["']/;

test("core modules stay free of node: and codec imports", () => {
	for (const file of CORE) {
		const text = readFileSync(join(import.meta.dirname, file), "utf8");
		const hit = FORBIDDEN.exec(text);
		assert.equal(hit, null, `${file} imports ${hit?.[0]}`);
	}
});

test("the forbidden-import pattern catches each shape it guards", () => {
	for (const line of ['import { x } from "node:zlib";', "import * as z from 'node:fs';", 'export * from "./codec.ts";', 'const m = import("./lz4.ts");', 'import "node:path";']) {
		assert.ok(FORBIDDEN.test(line), line);
	}
	assert.ok(!FORBIDDEN.test('import { y } from "./format.ts";'));
});
