# binpazer for Python

A Python implementation of the binpazer block-container format (see [SPEC.md](../SPEC.md)). It writes files byte-identical to the C reference for uncompressed and `stored` blocks, and it decodes every codec in the registry.

It needs Python 3.10 or newer and no package from PyPI. License: MIT, like the rest of the repository.

## Modules

- The core is every module of `binpazer/` except `codec.py`. It imports only the standard library.
- `binpazer/codec.py` is the codec layer. deflate uses the `zlib` module. zstd and lz4 call the system `libzstd` and `liblz4` through `ctypes`. The C implementation links the same libraries. Both are BSD-licensed. When one is not installed, its codec raises `codec_unavailable` and `lookup_codec` returns `None`.

The library opens no file. A reader takes any binary file object with `read`. A writer takes any with `write`. Each one seeks when `seekable()` is true: the reader then knows the input size, and the writer back-patches the header.

## Usage

```python
import io
import binpazer as bp

f = io.BytesIO()
w = bp.Writer(f, bp.parse_guid("aabbccdd-0000-0000-0000-000000000001"), "demo", [
	bp.TypeDef(1, bp.parse_guid("11223344-5566-7788-99aa-bbccddeeff00"), "Mesh"),
])
w.block(1, bp.FLAG_HAS_CRC, b"vertices")
bp.compress_block(w, 1, 0, bp.CODEC_ZSTD, None, bytes(10_000))
w.write_index()
w.end()

r = bp.Reader(io.BytesIO(f.getvalue()))
while (b := r.next()) is not None:
	print(b.name, len(bp.read_payload_decompressed(r, b)))
```

Read or skip each payload before the next `next()`. `find(type_id)` and `at(offset)` jump through the Block Index. `stream_payload` hands a large payload over in chunks.

Every failure is a `BinpazerError`. Switch on `err.code`, which uses the same names as the TypeScript package.

## Appendable files

An appendable file (spec 1.2) grows block by block while readers follow it. `next_committed()` returns a block only once it is whole and its CRC seal verifies. It returns `bp.PENDING` without moving the reader while a block is still on its way, and `None` after the End block.

```python
w = bp.Writer(open("log.binpazer", "wb"), writer_guid, "logger", types, appendable=True)
w.block(1, 0, record)  # sealed and flushed before it returns

r = bp.Reader(open("log.binpazer", "rb"))
b = r.next_committed()  # a Block, bp.PENDING, or None
if isinstance(b, bp.Block):
	print(r.read_payload(b))

writer, types = bp.resume_append_writer(open("log.binpazer", "r+b"))  # a later process continues the file
writer.end()
```

## Speed

CRC-32C is pure Python, because the standard library has no CRC-32C. It is the cost of every block with `has_crc`, and of every block of an appendable file. [`bench/RESULTS.md`](../bench/RESULTS.md) has the numbers next to the other implementations.

## Test

```sh
make -C py test
```

The interop test compiles `c/interop.c` with `cc` and links zlib, libzstd and liblz4 (`zlib1g-dev libzstd-dev liblz4-dev`). It fails when they are missing.
