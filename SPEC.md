# binpazer -- a flexible, block-based binary file format

binpazer is a block-based binary container format. A sequence of self-describing blocks follow a small fixed header. A reader can walk the file and skip blocks it does not understand. The layout is little-endian and 8-byte aligned so a C program can `mmap` the file and cast pointers straight into it.

_Headings in this document are intentionally unnumbered. A section can be added or removed without renumbering the rest. Reference sections by name._

## Design goals

- **Walkable without full understanding.** Every block carries its own length, so any reader can step over an unknown block. A per-block flag says whether skipping it is *semantically* safe.
- **Globally identifiable block types.** A GUID identifies a block type, unique across all programs forever. GUIDs are interned to small per-file integers so repeating a type 50,000 times costs a couple of bytes each, not 16.
- **mmap-friendly.** Fixed endianness, natural alignment, explicit offsets: point a `struct *` at the mapped bytes, no parsing or byte-swapping on common targets.
- **Forward/backward compatible.** Unknown blocks, unknown flag bits, and reserved fields are all ignorable, so old readers tolerate new files.

## Conventions

### Endianness

All multi-byte integers are little-endian. The dominant targets (x86-64, ARM64, RISC-V, WASM) are little-endian, so mmap + pointer-cast needs no byte-swapping there. Big-endian readers must swap.

### Alignment

`ALIGN = 8` bytes. The file header is a multiple of `ALIGN`. Every block starts on an 8-byte boundary, with zero padding inserted between blocks. A field of width W (1/2/4/8) always sits at an offset that is a multiple of W. A `struct` cast over a payload is well-defined. Padding is never counted in a `length` field.

It is recomputed via `align_up(x, 8) = (x + 7) & ~7`.

### Primitive types

| Name | Size | Description |
|---|---|---|
| `u8` `u16` `u32` `u64` | 1/2/4/8 | unsigned little-endian integer |
| `i8` `i16` `i32` `i64` | 1/2/4/8 | signed little-endian, two's complement |
| `f32` `f64` | 4/8 | IEEE-754 little-endian |
| `guid` | 16 | see [GUID](#guid) |
| `string` | variable | see [String](#string) |

Every header field is naturally aligned. A header maps directly to a C `struct` and can be `mmap`'d and cast on little-endian targets (amd64 and arm64 are both little-endian). The only sub-byte fields are the individual `flags` bits: read `flags` as a whole `u16` and test bits with masks. Do not model them as C `struct` bitfields, whose layout is not portable across compilers.

### GUID

A 16-byte value stored as several opaque bytes in canonical (RFC 4122 / big-endian text) order: one byte is the first hex pair of the textual UUID. The GUID is treated as an opaque blob. Its internal fields are never interpreted. (Some platforms' native GUID structs store the first fields little-endian -- those writers must byte-swap to canonical order on the way in and out.)

### String

Length-prefixed and null-terminated, so it can be `memcpy`'d by length or handed to C string APIs directly.

| Offset | Size | Field | Type | Notes |
|---|---|---|---|---|
| 0 | 4 | `byte_length` | u32 | UTF-8 byte count, excluding the terminator |
| 4 | `byte_length` | `data` | u8[] | UTF-8, no BOM |
| 4 + `byte_length` | 1 | `terminator` | u8 | always `0x00` |

On-disk size = `byte_length + 5`. `byte_length` counts bytes, not code points. The length prefix is authoritative. Embedded `0x00` bytes are permitted (a C-string view of such a string truncates early).

## File layout

```
+-------------------------------+  offset 0
| File header                   |  fixed fields + writer_name + pad
+-------------------------------+  offset = first_block_offset
| Block: Block Type Table       |  type_id = 65534, required, first block
+-------------------------------+
| Block ...                     |
| Block ...                     |
+-------------------------------+
| (optional footer)             |
+-------------------------------+  offset = file_length
```

## File header

A fixed-size part, followed by the length-prefixed `writer_name` and zero padding up to `first_block_offset`.

| Offset | Size | Field | Type | Notes |
|---|---|---|---|---|
| 0 | 8 | `magic` | char[8] | ASCII `binpazer` = `62 69 6E 70 61 7A 65 72` |
| 8 | 4 | `signature` | u8[4] | `0D 0A 1A 0A` -- corruption guard |
| 12 | 2 | `version_major` | u16 | bumped on incompatible changes |
| 14 | 2 | `version_minor` | u16 | bumped on backward-compatible additions |
| 16 | 16 | `writer_guid` | guid | GUID of the writing program |
| 32 | 8 | `file_length` | u64 | total file size; `0xFFFFFFFFFFFFFFFF` = unknown/streaming |
| 40 | 8 | `first_block_offset` | u64 | absolute offset of the first block |
| 48 | 4 | `header_flags` | u32 | see [Header flags](#header-flags) |
| 52 | 4 | `reserved` | u32 | must be 0 |
| 56 | var | `writer_name` | string | human-readable name of the writing program |
| ... | pad | `padding` | u8[] | zero pad up to `first_block_offset` (multiple of 8) |

The fixed part is many bytes. `first_block_offset` lets a reader jump straight to the blocks without parsing `writer_name`.

### Versions

| Version | Change |
|---|---|
| 1.0 | initial format |
| 1.1 | [Compression](#compression): block flag bit 2 and the Compression Envelope |
| 1.2 | [Appendable files](#appendable-files): header flag bit 0 and the End block |

A small bump is a backward-compatible addition: a 1.0 reader walks a 1.1 file correctly. This is because every 1.1 addition is announced by a flag bit or a block type it already knows how to skip. A writer emits the lowest `version_minor` its content requires. A file using no 1.1 feature still declares 1.0 and is byte-identical to what a 1.0 writer produced.

`signature` (`0D 0A 1A 0A`) catches the common transport corruptions: CRLF/LF translation, 7-bit stripping, and Ctrl-Z (`1A`) halting output on legacy DOS `type`.

### Header flags

| Bit | Name | Meaning |
|---|---|---|
| 0 | `appendable` | The file is an [appendable file](#appendable-files): a writer can still be adding blocks to it, and every block carries a commit seal. |
| 1.. | reserved | must be 0; readers MUST ignore unknown bits |

## Block header

Every block begins with an 8-byte little-endian header, directly castable as `struct { uint16_t type_id; uint16_t flags; uint32_t length; }`:

| Offset | Size | Field | Type | Notes |
|---|---|---|---|---|
| 0 | 2 | `type_id` | u16 | id from the Block Type Table; `0` and `65535` invalid; user types ascend from `1`, predefined types descend from `65534` |
| 2 | 2 | `flags` | u16 | see [Block flags](#block-flags) |
| 4 | 4 | `length` | u32 | payload size; unit depends on the `large_mode` flag |

The `large_mode` flag (bit 4) interprets length`:

- **`large_mode` clear:** `length` is a **byte** count. Max payload `2^32 - 1` (4 GiB).
- **`large_mode` set:** `length` is a count of **4096-byte units** (one page). Payload size = `length * 4096`. Max payload `(2^32 - 1) * 4096` (~16 TiB), in 4096-byte granularity.

Let `byte_len = large_mode ? length * 4096 : length`. The payload starts at `block_start + 8`. The next block is at `align_up(block_start + 8 + byte_len + (has_crc ? 4 : 0), 8)`.

Large mode trades byte-exact sizing for reach. A block that is both large and not a multiple of many bytes records its own trailing slack inside the payload (e.g. a leading `u8 padding_bytes`). The format does not track sub-page lengths in large mode. 4096 is the common OS page size, so large payloads stay page-aligned.

There is no version field in the header. A block type versions itself: mint a new GUID for an incompatible revision, or carry a version field inside the payload.

If `has_crc` is set, a 4-byte CRC-32C over `[block_start .. end of payload]` follows the payload, before alignment padding.

## Block flags

| Bit | Name | Meaning |
|---|---|---|
| 0 | `critical` | If set and the reader does not know this `type_id`'s GUID, the file cannot be correctly processed -- the reader MUST stop. If clear (ancillary), an unknown block is safe to skip. |
| 1 | `safe_to_copy` | If set, an editor that does not understand this block may copy it verbatim when rewriting the file. If clear, drop it on rewrite when not understood. |
| 2 | `compressed` | The stored payload is a [Compression Envelope](#compression), not the block's logical bytes. |
| 3 | `has_crc` | A 4-byte CRC-32C trailer follows the payload. |
| 4 | `large_mode` | `length` counts 4096-byte units instead of bytes (see [Block header](#block-header)). |
| 5.. | reserved | must be 0; readers MUST ignore unknown bits |

## Block Type Table

The first block in every file (`type_id = 65534`). It maps each interned `type_id` to its global `guid` and an optional human-readable name (so generic tools can label unknown blocks). Block *types* are few, so readers parse this once into a `type_id -> {guid, name}` map.

Payload:

| Offset | Size | Field | Type | Notes |
|---|---|---|---|---|
| 0 | 4 | `entry_count` | u32 | number of entries |
| 4 | 4 | `reserved` | u32 | must be 0 |
| 8 | var | `entries` | Entry[] | each 8-byte aligned |

Entry:

| Offset | Size | Field | Type | Notes |
|---|---|---|---|---|
| 0 | 2 | `type_id` | u16 | the id this entry defines |
| 2 | 2 | `entry_flags` | u16 | reserved, must be 0 |
| 4 | 16 | `guid` | guid | global identity of the block type |
| 20 | var | `name` | string | type name; may be empty |
| ... | pad | `padding` | u8[] | zero pad to 8-byte boundary |

- `type_id`s are arbitrary per-file ids: user types ascend from `1`, predefined types descend from `65534`. `0` and `65535` are invalid. Ids may be listed in any order, and the table may include entries no block uses (no penalty).
- A `type_id` MUST appear at most once. `0` and `65535` MUST NOT appear.
- The table Must include its own entry (`type_id = 65534` -> the BTT GUID) for full self-description, though a reader already knows the first block is the table by position.

## Well-known type ids and GUIDs

Reserved GUIDs live in the namespace `62696e70-617a-6572-0000-*` (the first several bytes spell `binpazer` in a hex dump).

| type_id | Meaning |
|---|---|
| 0 | invalid / none (zeroed memory is never a valid block) |
| 1, 2, 3, ... (ascending) | user block types |
| ..., 65532, 65533, 65534 (descending) | predefined types (below) |
| 65535 | invalid |

Predefined types occupy the top of the range, decrementing from 65534:

| type_id | GUID | Predefined type |
|---|---|---|
| 65534 | `62696e70-617a-6572-0000-000000000001` | Block Type Table (required, first) |
| 65533 | `62696e70-617a-6572-0000-000000000002` | Block Index (optional) |
| 65532 | `62696e70-617a-6572-0000-000000000003` | Writer Info (optional) |
| 65531 | `62696e70-617a-6572-0000-000000000004` | Padding / free space (optional) |
| 65530 | `62696e70-617a-6572-0000-000000000005` | Group (optional, nests blocks) |
| 65529 | `62696e70-617a-6572-0000-000000000006` | End (closes an [appendable file](#appendable-files)) |

User programs mint a random UUIDv4 per block type and reuse it across all files.

## Optional features

A flag or a well-known block gats each, so simple readers can ignore it.

### Group blocks (nesting)

The Group block (predefined `type_id = 65530`, GUID `...0005`) holds a sequence of blocks as its payload, giving hierarchy without a flag bit. A generic tool that recognizes the Group GUID descends into it. One that does not treats it as an opaque block and skips it via `length`. (Any block type whose handler knows its payload is sub-blocks can nest too -- the Group type just makes nesting explicit and type-agnostic.)

- A Group's children are normal blocks, 8-byte aligned, with offsets relative to the payload start. A reader parses them by recursing with the top-level logic.
- The parent's `length` spans all descendants, so a reader uninterested in a Group skips the entire subtree in one offset advance.
- Children resolve `type_id`s against the same file-global Block Type Table. There is no nested table.
- A Group payload is blocks only -- no type-defined prefix. Put any header data in a leaf child block at the front. Do this so even a tool that does not know the contents can enumerate the children.
- Readers Must enforce a maximum nesting depth against hostile or corrupt files.

### Block Index

The Block Index (predefined `type_id = 65533`, GUID `...0002`) is an optional, ancillary block that maps block `type_id`s to their absolute file offsets. A reader can jump straight to a block instead of walking the file. It is written *after* the blocks it indexes (their offsets must already be known) and is located via the [footer](#footer).

Payload:

| Offset | Size | Field | Type | Notes |
|---|---|---|---|---|
| 0 | 4 | `entry_count` | u32 | number of entries |
| 4 | 4 | `reserved` | u32 | must be 0 |
| 8 | var | `entries` | Entry[] | 16 bytes each |

Entry:

| Offset | Size | Field | Type | Notes |
|---|---|---|---|---|
| 0 | 2 | `type_id` | u16 | the indexed block's type id |
| 2 | 2 | `reserved` | u16 | must be 0 |
| 4 | 4 | `reserved` | u32 | must be 0 (room for a future length field) |
| 8 | 8 | `offset` | u64 | absolute file offset of that block's 8-byte header |

A `type_id` may appear in more than one entry -- one per indexed block instance. The index need not be exhaustive: it is a hint, and a block missing from it is still found by walking. Because offsets are file-position dependent, the index is **not** `safe_to_copy` -- drop or rebuild it when rewriting a file.

Readers Must use the index opportunistically: when the input is seekable and a [footer](#footer) is present, read the index. It seek directly to a requested block. This also covers otherwise fall back to a linear walk.

### Writer Info

The Writer Info block (predefined `type_id = 65532`, GUID `...0003`) is an optional home for `writer_name`. And other producer metadata when the file header is kept as a fixed-size struct.

### Compression

*Added in 1.1.* A block whose `compressed` flag (bit 2) is set stores a **Compression Envelope** in place of its logical payload. The flag only announces the envelope. Codec **identity** lives inside it. The format is not tied to one codec.

| Offset | Size | Field | Type | Notes |
|---|---|---|---|---|
| 0 | 2 | `codec_id` | u16 | see the registry below; `0` is invalid |
| 2 | 2 | `codec_flags` | u16 | reserved, must be 0 |
| 4 | 4 | `reserved` | u32 | must be 0 |
| 8 | 8 | `uncompressed_length` | u64 | exact size of the decoded payload |
| 16 | 16 | `codec_guid` | guid | **only when `codec_id == 65535`** |
| 16 (or 32) | var | `codec_data` | u8[] | the codec's own bytes |

The fixed part is several bytes (32 with the GUID escape), 8-byte aligned and naturally aligned field by field. It casts to a `struct` like every other header in this format.

| `codec_id` | Codec |
|---|---|
| 0 | invalid |
| 1 | `stored` -- identity; `codec_data` is the payload verbatim |
| 2 | `deflate` -- raw DEFLATE (RFC 1951), no zlib/gzip wrapper |
| 3 | `zstd` (RFC 8878) |
| 4 | `lz4` -- LZ4 frame format |
| 5..65534 | unassigned; a future revision of this document assigns them |
| 65535 | codec identified by `codec_guid`, for codecs outside this registry |

`stored` exists so every implementation can write and read a well-formed envelope with no external dependency. So a writer can decide per block not to compress without changing the block's shape.

**Encoder output is implementation-defined.** A codec id names a *decodable format*, not one encoder: conforming writers may compress same input to different `codec_data` of different lengths. Both files are correct. An implementation is conformant when it can DECODE every codec in this registry, not because its encoded bytes match anyone else's. Byte-for-byte agreement between implementations is required only for uncompressed blocks and for `stored`, where no encoder is involved.

Rules:

- Consider the block's `length` field. That field always describes the **stored** bytes -- the envelope, whole -- so block walking, skipping, `large_mode`. The Block Index and the next-block formula are all unaffected by compression. A reader that does not implement compression still traverses the file correctly.
- `has_crc` covers the stored bytes (the envelope), not the decoded payload: the CRC verifies what is on disk, before any codec runs.
- `uncompressed_length` is authoritative. A reader MUST treat a decode that yields a different length as corruption, and Must use the field to bound its allocation *before* decoding (a decoder is otherwise an unbounded allocator driven by untrusted bytes).
- A reader that does not implement a block's `codec_id` MUST NOT interpret its payload. It may still skip the block, copy it verbatim when `safe_to_copy` is set, and report the codec. The same applies to a pre-1.1 reader, which sees bit 2 as a reserved bit: it walks. It skips correctly but must not read the payload of a block whose flag bits it does not all understand.
- Compression is per block. Mixing compressed and uncompressed blocks in one file is normal and expected. The [Block Type Table](#block-type-table), [Block Index](#block-index) and [footer](#footer) are never compressed. The structural path thus stays directly mappable.
- Nothing about an uncompressed block changes in 1.1. A file with no compressed block is byte-identical to the 1.0 file with the same content.

### Appendable files

*Added in 1.2.* An appendable file is a file that a writer keeps adding blocks to, and its total length is not known. A reader can read it at the same time. The model is a JSON Lines file: a record is whole, or the reader does not see it. A line of that format ends with a newline. A block of this format ends with its CRC trailer, and that trailer is the **commit seal**.

A writer marks the file with the `appendable` [header flag](#header-flags) and declares version 1.2. `file_length` stays at the unknown sentinel while the file is open.

#### Committed blocks

A block of an appendable file is **committed** when both conditions hold:

- Every byte of the block is in the file: the header, the payload, the CRC trailer, and the alignment padding.
- The block sets `has_crc`, and the CRC verifies.

Bytes after the last committed block are **in progress**. They are not data yet. A reader MUST NOT give its caller any part of a block that is not committed.

A reader that stands at a block boundary sees one of these states:

| What is at the boundary | State | What the reader does |
|---|---|---|
| a committed block | committed | handle the block, go to the next boundary |
| the end of the file | in progress | wait, then look again at the same offset |
| a partial header, or a block that runs past the end of the file | in progress | wait, then look again at the same offset |
| a header with `type_id` = `0` | in progress | wait: zeroed space is where a block will be |
| all bytes present, but `has_crc` clear or the CRC incorrect | torn | do not use it; wait, or report it |
| a committed [End block](#end-block) | ended | stop: no block follows |

A **torn** block is the tail that an interrupted writer left, or it is damage. The bytes do not say which. A reader treats it like an in-progress block, because a writer that [resumes](#resume) the file replaces it.

The end of the file without an End block means that the writer is still open, or that it stopped without closing. A reader cannot tell these apart.

A reader of a pipe or a socket needs none of this. Its blocks arrive in order and its reads wait for the bytes. The End block tells it that the writer closed the stream on purpose. An end of input before the End block means the writer stopped early.

#### Writer rules

- Set `has_crc` on every block after the Block Type Table. The table has no seal. It is complete when all its bytes are in the file, and no block can be committed before it.
- Write each block after the one before it. Never change a committed byte.
- Only one writer appends at a time.
- Write no [Block Index](#block-index) and no [footer](#footer). A file that grows has no fixed end to put a footer at.
- List the End type in the Block Type Table, so a reader older than 1.2 can label the block.
- The block types of the file are fixed when the file is created, because the Block Type Table is written one time.

A writer does not need to write a block in one piece. A reader accepts a block only when all of it is there and the seal verifies. A block that arrives in parts is in progress until the last part lands.

A commit makes a block visible to readers. It does not make the block durable. A writer that needs the block to survive a power loss must also sync the file.

#### End block

The End block (predefined `type_id = 65529`, GUID `...0006`) closes an appendable file. Its payload is empty and its flags are `has_crc` only. No block follows it. A writer that can seek also back-patches `file_length` after it writes the End block. An ended file accepts no more blocks.

A writer that only stops, with no End block, leaves the file open. Another writer can resume it later.

#### Resume

A writer resumes an open appendable file in these steps:

1. Read the header and the Block Type Table. Stop if the `appendable` flag is clear.
2. Walk the committed blocks to the first boundary that holds no committed block. Stop if the walk finds the End block.
3. If bytes follow that boundary, they are a torn tail. Cover all of them with one or more [Padding](#well-known-type-ids-and-guids) blocks (`type_id = 65531`) that are sealed like any other block. Each payload byte of a Padding block is `0x00`.
4. Append new blocks after the last committed block or the last Padding block.

The Padding blocks are not optional. A torn tail can contain a complete sealed block of its own, for example a child of a torn [Group](#group-blocks-nesting). A short new block written over the start of the tail can leave that stale block at the next boundary. A reader then takes it for committed data.

### Footer

An optional fixed 16-byte footer at the very end points to the [Block Index](#block-index), so a reader can find it without a forward walk. It is written when an index is present. A reader checks the last several bytes for the magic `rezapnib`. If it matches, `index_block_offset` is the absolute offset of the Block Index block's 8-byte header.

| Offset (from end) | Size | Field | Type |
|---|---|---|---|
| -16 | 8 | `index_block_offset` | u64 |
| -8 | 8 | `footer_magic` | char[8] = `rezapnib` |

## Reader algorithm

1. Verify `magic` (`binpazer`) and `signature` (`0D 0A 1A 0A`). Reject on mismatch.
2. If `version_major` exceeds the highest you support, refuse.
3. Read the fixed header. Take `first_block_offset` and `file_length`.
4. At `first_block_offset`, parse the block. It MUST be `type_id == 65534`. Build the `type_id -> {guid, name}` map.
5. Walk blocks. For each: extract `type_id`, `flags`, `length`. This also covers resolve the GUID.
   - Known GUID: handle it.
   - Unknown GUID, `critical` set: stop -- the file needs a feature you lack.
   - Unknown GUID, `critical` clear: skip it.
   - If `has_crc`, optionally verify before trusting the payload.
   - If `compressed`, the payload is a [Compression Envelope](#compression): decode it with the envelope's codec, or leave the block uninterpreted.
   - Advance using the next-block formula in [Block header](#block-header).
6. Stop at `file_length` (or EOF when `file_length` is the unknown sentinel).

Readers MUST ignore unknown flag bits and reserved fields.

A file with the `appendable` header flag can grow while it is read. A reader that reads such a file while a writer is open follows [Committed blocks](#committed-blocks) in step 5. It handles a block only after the block is committed. It stops at the End block.

## Writer rules

- Emit the file header, then the Block Type Table, then blocks.
- 8-byte-align every block and zero the padding.
- Set `critical` only on blocks a reader truly must understand. Prefer ancillary.
- Use `large_mode` for payloads over 4 GiB, otherwise leave it clear.
- Back-patch `file_length` (and the footer/index, if used) after writing, or use the streaming sentinel.
- Reuse a stable GUID per block type across all files.
- For a file that grows with no known end, write an [appendable file](#appendable-files) and follow its writer rules.

## Validation

- `magic` and `signature` match exactly.
- The first block is `type_id == 65534`.
- No duplicate `type_id` in the Block Type Table, do not `0` or `65535`.
- Every block's `type_id` is present in the table. Predefined ids (65530-65534) are implicitly defined by this spec and exempt -- writers MAY emit predefined blocks (e.g. the Block Index) without a table entry.
- Reserved fields and bits are 0 on write (ignored on read).
- Blocks are 8-byte aligned. The last block ends at `file_length` (when not the streaming sentinel).
- 0x00 follows a `string`'s `byte_length` bytes`.
- A block with the `compressed` flag carries a whole [Compression Envelope](#compression). Its stored payload is at least the bytes of a later fixed (32 with the GUID escape), `codec_id` is not 0, and `codec_flags`/`reserved` are 0. An unassigned `codec_id` is not a validation failure -- it is a file from a later revision -- but a validator Must report it.
- In an [appendable file](#appendable-files), every block after the Block Type Table sets `has_crc`.
- An appendable file holds no Block Index block and no footer.
- The End block appears only in an appendable file. Its payload is empty, and no block follows it.
- An appendable file with no End block can end in bytes that are in progress. Those bytes are not a failure. A validator reports where they start. A torn block is a failure: the file needs a [resume](#resume).

## Example

A file written by program "demo" with one user block of type `ExampleBlock` whose payload is the ASCII bytes `hello, file!` (small mode: `length` in bytes).

| Offset | Size | Region | Detail |
|---|---|---|---|
| 0 | 56 | file header (fixed) | `file_length`=192, `first_block_offset`=72, version 1.0 |
| 56 | 9 | `writer_name` | `04 00 00 00` + `demo` + `00` |
| 65 | 7 | padding | zero, to offset 72 |
| 72 | 8 | block 1 header | `type_id`=65534, `flags`=`critical`, `length`=88 |
| 80 | 8 | BTT payload head | `entry_count`=2, `reserved`=0 |
| 88 | 40 | BTT entry (BTT) | id=65534, BTT GUID, name `BlockTypeTable` (padded) |
| 128 | 40 | BTT entry (ExampleBlock) | id=1, ExampleBlock GUID, name `ExampleBlock` (padded) |
| 168 | 8 | block 2 header | `type_id`=1, `flags`=ancillary, `length`=12 |
| 176 | 12 | block 2 payload | `hello, file!` |
| 188 | 4 | padding | zero, to offset 192 |
| 192 | -- | end | `= file_length` |

First several bytes:

```
62 69 6E 70 61 7A 65 72  0D 0A 1A 0A
 b  i  n  p  a  z  e  r  \r \n  ^Z \n
```

Block 1's header bytes (`type_id`=65534=`0xFFFE`, `flags`=`0x0001`, `length`=88): `FE FF 01 00 58 00 00 00`.
