# binpazer -- a flexible, block-based binary file format

**Status:** DRAFT / request-for-comment. Everything here is negotiable; this
document is the shared whiteboard. Sections marked **[OPEN]** are decisions I
have made a recommendation on but that still want your sign-off, and
**[DEVIATION]** marks a place where I changed something from the original notes
and explain why.

binpazer is a container format: a small fixed header followed by a sequence of
self-describing **blocks**. A reader can walk the whole file, skip blocks it does
not understand, and still make sense of the rest -- like PNG/RIFF, pushed
further. The on-disk layout is chosen so a C program can `mmap()` the file and
cast pointers straight into it.

---

## 1. Design goals

1. **Walkable without full understanding.** Every block carries its own length,
   so any reader can step over an unknown block. A per-block flag says whether
   skipping is *semantically safe* (see [critical vs ancillary](#62-block-flags)).
2. **Globally identifiable block types.** A block type is identified by a GUID
   that is unique across all programs, forever. GUIDs are interned to small
   per-file integers so repeating a block type 50,000 times costs 2 bytes each,
   not 16.
3. **mmap-friendly.** Fixed endianness, natural alignment, and explicit offsets
   mean a reader can point a `struct *` at the mapped bytes with no parsing or
   byte-swapping on the common platforms.
4. **Forward/backward compatible.** Unknown blocks, unknown flags, and reserved
   fields are all defined to be ignorable, so old readers tolerate new files.

---

## 2. Prior art (does this already exist?)

No single existing format is exactly this, but nearly every idea has precedent.
The honest summary: **binpazer is RIFF/IFF with GUID-identified block types (as
in MP4 `uuid` boxes), PNG-style skip-safety flags, and mmap-grade alignment.**

| Format | What it shares with binpazer | What binpazer does differently |
|---|---|---|
| **IFF** (EA, 1985) | chunk = type + length + payload; group chunks for nesting; pad to alignment | little-endian; GUID type ids; u64 lengths |
| **RIFF** (WAV/AVI/WebP) | little-endian IFF; `LIST` nesting; the canonical "chunked container" | 4cc -> GUID+interning; explicit flags; 64-bit |
| **PNG** | critical/ancillary/private/safe-to-copy semantics; per-chunk CRC | those 4 bits are explicit flags, not letter-casing; arbitrary type count |
| **ISO BMFF / MP4** | box = size + type; `uuid` boxes identify private types by 16-byte UUID; `size==1` -> 64-bit largesize | GUIDs are the *norm* (interned), not an escape hatch |
| **Matroska / EBML** | extensible, skip-unknown, tree-structured | fixed-width fields instead of varints (mmap/casting) |
| **TIFF** | u16 tag ids; endianness marker at offset 0 | sequential blocks, not an IFD offset table (though see optional index) |
| **Protocol Buffers** | tag-length-value; unknown fields are skippable | a file container, not a message encoding |
| **ELF** | string/symbol tables intern repeated identifiers | the Block Type Table is that idea applied to block types |

**The distinctive combination** is the GUID->u16 interning table plus explicit
skip-safety flags plus mmap alignment plus length-prefixed-*and*-null-terminated
strings. If a drop-in standard is acceptable, **RIFF or MP4 already do 80% of
this**; binpazer earns its keep only if the GUID-global-identity + zero-copy
goals matter to you. Worth deciding before we build tooling.

---

## 3. Conventions

### 3.1 Endianness **[OPEN] [DEVIATION -- not in original notes]**

All multi-byte integers are **little-endian**. This was unspecified in the notes
and *must* be pinned down for a binary format. Little-endian is recommended
because the dominant targets (x86-64, ARM64, RISC-V, WASM) are little-endian, so
mmap + pointer-cast needs zero byte-swapping there. Big-endian readers must swap.

A byte-order *marker* is therefore unnecessary, but we still include a corruption
guard (see [signature](#5-file-header)).

### 3.2 Alignment **[OPEN] [DEVIATION -- not in original notes]**

`ALIGN = 8` bytes. This is the other thing you need for real mmap use and it was
not in the notes.

- The file header is sized to a multiple of `ALIGN`.
- Every block starts on an `ALIGN`-byte boundary; writers insert zero padding
  between blocks to achieve this.
- A field of width W (W in {1,2,4,8}) is always at an offset that is a multiple
  of W, so a `struct` cast over a block payload is well-defined.

Padding bytes are not counted in any `length` field; they are recomputed by the
reader via `align_up(x, 8) = (x + 7) & ~7`.

### 3.3 Primitive types

| Name | Size | Description |
|---|---|---|
| `u8` `u16` `u32` `u64` | 1/2/4/8 | unsigned little-endian integer |
| `i8` `i16` `i32` `i64` | 1/2/4/8 | signed little-endian, two's complement |
| `f32` `f64` | 4/8 | IEEE-754 little-endian |
| `guid` | 16 | see [3.4](#34-guid) |
| `string` | variable | see [3.5](#35-string) |

### 3.4 `guid`

A 16-byte value, stored as **16 opaque bytes in canonical (RFC 4122 / big-endian
text) order** -- i.e. byte 0 is the first hex pair of the textual UUID.

> **[FLAG] GUID byte-order footgun.** Microsoft's GUID struct stores the first
> three fields little-endian, so a naive `memcpy` of a Windows `GUID` produces a
> *different* 16 bytes than the canonical UUID. binpazer mandates canonical order
> and treats the GUID as an opaque blob (never interpreting its internal fields)
> precisely to dodge this. Windows writers must byte-swap fields 1-3 on the way
> in and out.

### 3.5 `string`

Length-prefixed **and** null-terminated, so the payload can be `memcpy`'d by
length or handed to C string APIs directly.

| Offset | Size | Field | Type | Notes |
|---|---|---|---|---|
| 0 | 4 | `byte_length` | u32 | number of UTF-8 bytes, **excluding** the terminator |
| 4 | `byte_length` | `data` | u8[] | UTF-8, no BOM |
| 4 + `byte_length` | 1 | `terminator` | u8 | always `0x00` |

On-disk size = `byte_length + 5`. The encoding is UTF-8; `byte_length` counts
bytes, not code points.

> **[FLAG] embedded NULs.** The length prefix is authoritative. Strings *may*
> contain embedded `0x00`, but then the C-string view truncates early -- so
> embedding NULs is allowed but discouraged. If we want to forbid them outright
> (cleaner C interop guarantee), say so and I will make it a hard constraint.

---

## 4. File layout

```
+-------------------------------+  offset 0
| File Header                   |  fixed fields + writer_name + pad
+-------------------------------+  offset = first_block_offset
| Block: Block Type Table       |  type_id = 1, REQUIRED, must be first block
+-------------------------------+
| Block ...                     |
| Block ...                     |
+-------------------------------+
| (optional footer)             |  see 9.4
+-------------------------------+  offset = file_length
```

---

## 5. File header

The first part is fixed-size (a POD struct you can cast); it is followed by the
length-prefixed `writer_name` and then zero padding up to `first_block_offset`.

| Offset | Size | Field | Type | Notes |
|---|---|---|---|---|
| 0 | 8 | `magic` | char[8] | ASCII `binpazer` = `62 69 6E 70 61 7A 65 72` |
| 8 | 4 | `signature` | u8[4] | `0D 0A 1A 0A` -- corruption guard (see below) |
| 12 | 2 | `version_major` | u16 | bumped on **incompatible** changes |
| 14 | 2 | `version_minor` | u16 | bumped on backward-compatible additions |
| 16 | 16 | `writer_guid` | guid | GUID of the program that wrote the file |
| 32 | 8 | `file_length` | u64 | total file size in bytes; `0xFFFFFFFFFFFFFFFF` = unknown/streaming |
| 40 | 8 | `first_block_offset` | u64 | absolute offset of the first block (the Block Type Table) |
| 48 | 4 | `header_flags` | u32 | reserved file-level flags, must be 0 |
| 52 | 4 | `reserved` | u32 | must be 0 |
| 56 | var | `writer_name` | string | human-readable name of the writing program |
| ... | pad | `padding` | u8[] | zero pad up to `first_block_offset` (multiple of 8) |

Fixed part = 56 bytes. `first_block_offset` lets a reader skip straight to the
blocks without parsing `writer_name`, so the variable field does not break
walkability.

**`signature` rationale (borrowed from PNG):** `0D 0A 1A 0A` detects the most
common transport corruptions -- CRLF<->LF translation (the `0D 0A` / `0A`),
7-bit stripping, and `1A` (Ctrl-Z) halts output on legacy DOS `type`.

> **[OPEN] writer_name placement.** Keeping a variable-length field in the header
> means the header is not a single fixed-size `struct`. If you want the header to
> be pure POD, I will move `writer_name` (and optionally `writer_guid`) into a
> well-known [Writer Info](#92-writer-info-optional) block and make the header
> exactly 56 bytes. Both are supported by `first_block_offset`; tell me which you
> prefer. My lean: move it out, keep the header POD.

> **[FLAG] file_length vs streaming.** A writer that streams to a pipe cannot
> know the length up front. Hence the `0xFFFF...FF` sentinel. Readers that need
> truncation detection should compare `file_length` to the actual size when it is
> not the sentinel.

---

## 6. Block

The recommended block header is **16 bytes**, every byte used, naturally aligned.

### 6.1 Block header (recommended) **[DEVIATION -- see 6.3]**

| Offset | Size | Field | Type | Notes |
|---|---|---|---|---|
| 0 | 2 | `type_id` | u16 | local id from the Block Type Table; `0` = invalid, `1` = Block Type Table |
| 2 | 2 | `flags` | u16 | see [6.2](#62-block-flags) |
| 4 | 2 | `type_version` | u16 | version of *this block type's* payload schema |
| 6 | 2 | `reserved` | u16 | must be 0 |
| 8 | 8 | `payload_length` | u64 | payload size in bytes, **excluding** header, CRC, and padding |
| 16 | `payload_length` | `payload` | u8[] | block-type-specific |
| 16 + `payload_length` | 0 or 4 | `crc` | u32 | present iff `flags.has_crc`; CRC-32C over bytes `[block_start .. end of payload]` |
| ... | pad | `padding` | u8[] | zero pad so the next block is 8-byte aligned |

Next block offset:
`align_up(block_start + 16 + payload_length + (has_crc ? 4 : 0), 8)`.

### 6.2 Block flags

This is the answer to "maybe put something else here?" in the notes: the spare
u16 is a **flags** field. The first two bits are the mechanism that makes
"parse without understanding everything" actually safe.

| Bit | Name | Meaning |
|---|---|---|
| 0 | `critical` | If set and the reader does not know this `type_id`'s GUID, the file cannot be correctly processed -- the reader MUST stop/error. If clear (**ancillary**), an unknown block is safe to skip. |
| 1 | `safe_to_copy` | If set, an editor that does not understand this block may copy it verbatim when rewriting the file. If clear, the block depends on file/other-block state and should be dropped on rewrite if not understood. |
| 2 | `compressed` | Payload is compressed; it begins with a small compression descriptor (codec id + uncompressed size). See [9.3](#93-compression-optional). |
| 3 | `has_crc` | A 4-byte CRC-32C trailer follows the payload (see table above). |
| 4 | `container` | The payload is itself a sequence of binpazer blocks (enables trees). See [9.1](#91-nesting--container-blocks). |
| 5..15 | reserved | must be 0; readers MUST ignore unknown bits |

> **[FLAG] this is the key addition to your notes.** The original block had no
> way to tell a reader whether skipping an unknown block is *safe*. With only a
> length you can always step over a block, but you cannot know if doing so
> silently corrupts your understanding of the file. PNG solved exactly this with
> its ancillary bit; bits 0-1 above are that solution. Without them, goal #1
> ("walkable without full understanding") is not actually met.

### 6.3 Compact block header (alternative) **[OPEN]**

Your original notes specified an **8-byte** header. Here it is as the compact
variant:

| Offset | Size | Field | Type |
|---|---|---|---|
| 0 | 2 | `type_id` | u16 |
| 2 | 2 | `flags` | u16 |
| 4 | 4 | `payload_length` | u32 |

| | 16-byte (recommended) | 8-byte (compact) |
|---|---|---|
| per-block overhead | 16 B | 8 B |
| max payload | 16 EiB | 4 GiB |
| `type_version` field | yes | no |
| 50k blocks of headers | 800 KB | 400 KB |

**My recommendation: the 16-byte header.** The extra 8 bytes per block buys a
u64 length (no 4 GiB wall for, say, an embedded video or dataset block) and a
per-block schema version, and it keeps everything 16-byte aligned. The compact
header only wins when you have a huge number of tiny blocks; even then 400 KB of
savings is rarely decisive. If you know your files are millions-of-tiny-blocks,
we go compact. This is the single biggest fork in the spec -- see the question at
the end.

---

## 7. Block Type Table (type_id = 1)

The first block in every file. Maps each interned `type_id` to its global `guid`
(and an optional human-readable name, so generic tools can label unknown blocks).
There are few block *types* (not instances), so variable-length entries with
names are cheap; readers parse this once into a `type_id -> {guid, name}` map.

**Payload:**

| Offset | Size | Field | Type | Notes |
|---|---|---|---|---|
| 0 | 4 | `entry_count` | u32 | number of entries |
| 4 | 4 | `reserved` | u32 | must be 0 |
| 8 | var | `entries` | Entry[] | `entry_count` entries, each 8-byte aligned |

**Entry:**

| Offset | Size | Field | Type | Notes |
|---|---|---|---|---|
| 0 | 2 | `type_id` | u16 | the interned id this entry defines |
| 2 | 2 | `entry_flags` | u16 | reserved, must be 0 |
| 4 | 16 | `guid` | guid | global identity of the block type |
| 20 | var | `name` | string | human-readable type name; may be empty |
| ... | pad | `padding` | u8[] | zero pad to 8-byte boundary before the next entry |

Notes / rules:
- `type_id` values are an arbitrary per-file counter. By convention `1` is this
  table, user types start at `2`, but the table may list ids in any order and may
  include entries that are never used by any block (no penalty, as in the notes).
- A `type_id` MUST appear at most once. `type_id = 0` MUST NOT appear.
- The table SHOULD include its own entry (`type_id = 1` -> the well-known BTT
  GUID) so the file is fully self-describing, but a reader already knows block 1
  is the table by position.

---

## 8. Reserved type ids and well-known GUIDs

Reserved GUIDs live in the namespace `62696e70-617a-6572-0000-*` (the first 8
bytes spell `binpazer` in a hex dump -- deterministic, no randomness needed).

| type_id (convention) | GUID | Meaning |
|---|---|---|
| 0 | -- | invalid / none (so zeroed memory is never a valid block) |
| 1 | `62696e70-617a-6572-0000-000000000001` | Block Type Table (required, first) |
| (any) | `62696e70-617a-6572-0000-000000000002` | Block Index (optional, [9.2]) |
| (any) | `62696e70-617a-6572-0000-000000000003` | Writer Info (optional, [9.2]) |
| (any) | `62696e70-617a-6572-0000-000000000004` | Padding / Free space (optional) |
| 2 .. 65535 | program-assigned | user block types |

User programs mint their own random UUIDv4 for each block type they define and
reuse that same GUID across all files, forever.

---

## 9. Optional features

These are not required to read a basic file; each is gated by a flag or a
well-known block so simple readers can ignore them.

### 9.1 Nesting / container blocks

If `flags.container` is set, the block payload is itself a sequence of binpazer
blocks (same 16-byte header, same alignment, offsets relative to the payload
start). This gives RIFF-`LIST`-style trees. A reader that does not care about a
subtree skips the whole thing via the parent's `payload_length`.

### 9.2 Block Index / Writer Info

- **Block Index** (well-known GUID `...0002`): an ancillary block mapping each
  `type_id` to an array of `(offset: u64, length: u64)` for O(1) random access in
  large files without a full walk. Optional; when absent, readers walk linearly.
- **Writer Info** (`...0003`): holds `writer_name` (and anything else about the
  producer) if we decide to keep the file header pure POD (see [5] OPEN).

### 9.3 Compression

If `flags.compressed` is set, the payload begins with:

| Offset | Size | Field | Type | Notes |
|---|---|---|---|---|
| 0 | 2 | `codec_id` | u16 | 1 = zstd, 2 = lz4, 3 = deflate (registry TBD) |
| 2 | 2 | `reserved` | u16 | 0 |
| 4 | 8 | `uncompressed_length` | u64 | size after decompression |
| 16 | ... | `compressed_data` | u8[] | |

### 9.4 Footer (optional) **[OPEN]**

For very large files, an optional fixed 16-byte footer at the very end lets a
reader `mmap` and seek to `file_length - 16` to find the Block Index without a
forward walk (the ZIP "end of central directory" trick):

| Offset (from end) | Size | Field | Type |
|---|---|---|---|
| -16 | 8 | `index_block_offset` | u64 |
| -8 | 8 | `footer_magic` | char[8] = `rezapnib` (`binpazer` reversed) |

I have left this optional and would only add it if random access into huge files
is a real requirement.

---

## 10. Reader algorithm

1. Read and verify `magic` (`binpazer`) and `signature` (`0D 0A 1A 0A`). Reject
   on mismatch.
2. If `version_major` > the highest you support, refuse (incompatible).
3. Read the fixed header; take `first_block_offset` and `file_length`.
4. At `first_block_offset`, parse the block; it MUST be `type_id == 1` (Block
   Type Table). Build the `type_id -> {guid, name}` map.
5. Walk blocks from there. For each block:
   - Read the 16-byte header.
   - Resolve `type_id` -> GUID via the map.
   - **Known GUID:** handle it (dispatch on GUID, honoring `type_version`).
   - **Unknown GUID + `critical` set:** stop/error -- the file needs a feature
     you lack.
   - **Unknown GUID + `critical` clear:** skip it.
   - If `has_crc`, optionally verify before trusting the payload.
   - Advance to `align_up(start + 16 + payload_length + (has_crc?4:0), 8)`.
6. Stop at `file_length` (or EOF if `file_length` is the unknown sentinel).

Readers MUST ignore unknown `flags` bits and any reserved field.

---

## 11. Writer guidance

- Emit the file header, then the Block Type Table (block 1), then blocks.
- 8-byte-align every block; zero the padding.
- Set `critical` only on blocks a conforming reader truly must understand. Prefer
  ancillary so old readers keep working.
- Back-patch `file_length` (and footer/index, if used) after the body is written,
  or use the streaming sentinel.
- Reuse stable GUIDs per block type across all files your program writes.

---

## 12. Validation rules

- `magic` and `signature` exact match.
- First block is `type_id == 1`.
- No duplicate `type_id` in the Block Type Table; never `type_id == 0`.
- Every block's `type_id` is present in the table.
- All reserved fields are 0 on write (ignored on read).
- Block offsets are 8-byte aligned; the final block ends at `file_length` (when
  not the streaming sentinel).
- `string.byte_length` bytes are followed by a `0x00`.

---

## 13. Worked example (a tiny file)

A file written by program "demo" containing one user block of type
`ExampleBlock` whose payload is the 12 ASCII bytes `hello, file!`.

| Offset | Size | Region | Detail |
|---|---|---|---|
| 0 | 56 | file header (fixed) | `magic`, `signature`, version 1.0, `writer_guid`, `file_length`=208, `first_block_offset`=72 |
| 56 | 9 | `writer_name` | `04 00 00 00` + `demo` + `00` |
| 65 | 7 | padding | zero, to reach offset 72 |
| 72 | 16 | block 1 header | `type_id`=1, `flags`=critical, `type_version`=1, `payload_length`=88 |
| 88 | 8 | BTT payload head | `entry_count`=2, `reserved`=0 |
| 96 | 40 | BTT entry (type 1) | id=1, BTT GUID, name `"BlockTypeTable"` (padded to 40) |
| 136 | 40 | BTT entry (type 2) | id=2, ExampleBlock GUID, name `"ExampleBlock"` (padded to 40) |
| 176 | 16 | block 2 header | `type_id`=2, `flags`=ancillary, `type_version`=1, `payload_length`=12 |
| 192 | 12 | block 2 payload | `hello, file!` |
| 204 | 4 | padding | zero, to reach offset 208 |
| 208 | -- | end | `= file_length` |

First 12 bytes, hex:

```
62 69 6E 70 61 7A 65 72  0D 0A 1A 0A
 b  i  n  p  a  z  e  r  \r \n  ^Z \n
```

---

## 14. Open questions (please weigh in)

1. **Block header size: 16-byte (recommended) vs 8-byte compact?** Biggest fork;
   drives the 4 GiB-per-block limit and whether we get `type_version`.
2. **Endianness:** confirm little-endian is fine (recommended).
3. **`writer_name` in the header vs a Writer Info block?** Affects whether the
   header is a pure castable struct.
4. **CRC / compression / index / footer:** keep as optional features, or cut any
   of them from v1 to stay lean?
5. **Embedded NULs in strings:** allow (length is authoritative) or forbid for a
   stronger C-interop guarantee?
