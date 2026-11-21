# binpazer for TypeScript

A TypeScript implementation of the binpazer block-container format (see [SPEC.md](../SPEC.md)). It writes files byte-identical to the C reference for uncompressed and `stored` blocks, and it decodes every codec in the registry.

## Modules

- `src/core.ts` is the core: writer, reader, groups, envelopes and in-memory IO. It imports nothing from Node. It runs in a browser.
- `src/index.ts` is the core plus the codec layer (`src/codec.ts`), which uses `node:zlib` for deflate and zstd and a built-in LZ4 frame codec.

The library does no file IO. A `Source` (`read`, optional `seek` and `size`) and a `Sink` (`write`, optional `seek`) carry the bytes. `MemorySource` and `MemorySink` cover the in-memory case.

## Usage

```ts
import { CODEC_ZSTD, FLAG_HAS_CRC, MemorySink, MemorySource, Reader, Writer, compressBlock, parseGuid, readPayloadDecompressed } from "./src/index.ts";

const sink = new MemorySink();
const w = new Writer(sink, parseGuid("aabbccdd-0000-0000-0000-000000000001"), "demo", [
	{ typeId: 1, guid: parseGuid("11223344-5566-7788-99aa-bbccddeeff00"), name: "Mesh" },
]);
w.block(1, FLAG_HAS_CRC, new TextEncoder().encode("vertices"));
compressBlock(w, 1, 0, CODEC_ZSTD, null, new Uint8Array(10_000));
w.writeIndex();
w.end();

const r = new Reader(new MemorySource(sink.bytes()));
for (let b = r.next(); b !== null; b = r.next()) {
	console.log(b.name, readPayloadDecompressed(r, b).length);
}
```

Errors are `BinpazerError` instances. Switch on `err.code`.

## Appendable files

An appendable file (spec 1.2) grows block by block while readers follow it. `nextCommitted()` returns a block only once it is whole and its CRC seal verifies. It returns `"pending"` without moving the reader while a block is still on its way, and `null` after the End block.

```ts
import { MemoryFile, Reader, Writer, resumeAppendWriter } from "./src/index.ts";

const file = new MemoryFile();
const w = new Writer(file, writerGuid, "logger", types, { appendable: true });
w.block(1, 0, record); // sealed and flushed before it returns

const r = new Reader(file.view());
const b = r.nextCommitted(); // a Block, "pending", or null
if (b !== null && b !== "pending") console.log(r.readPayload(b));

const { writer } = resumeAppendWriter(file); // a later process continues the file
writer.end();
```

## Build and test

```sh
cd ts
ts0 test    # type-check, then run every src/*.test.ts with node:test
ts0 build   # type-check and emit dist/
```

The interop test compiles `c/interop.c` with `cc` and links zlib, libzstd and liblz4 (`zlib1g-dev libzstd-dev liblz4-dev`). It fails when they are missing.
