# bin-file-fmt

**binpazer** -- a flexible, block-based binary container format. A small fixed
header is followed by a sequence of self-describing blocks, so a reader can walk
the file and skip blocks it does not understand. The layout is little-endian and
8-byte aligned so a C program can `mmap` the file and cast pointers into it.

See **[SPEC.md](./SPEC.md)** for the format.

## Implementations

Minimal-dependency libraries that do **no file IO and no mmap** -- the caller
injects IO via callbacks (C/C++) or `io.Reader`/`io.Writer` (Go).

| Dir | Language | IO | Test |
|---|---|---|---|
| [`c/`](./c) | C11, allocation-free | `read`/`write` (+ optional `seek`) function pointers | `make -C c test` |
| [`cpp/`](./cpp) | C++17, header-only convenience wrapper over the C core | `std::function` callbacks | `make -C cpp test` |
| [`go/`](./go) | pure Go library (the CLI uses cobra) | `io.Reader` / `io.Writer` (`io.Seeker` optional) | `go-toolchain` (in `go/`) |

All three implement **every codec the format defines**, so no implementation
can write a file another cannot read. In C and C++ the codecs are a second file
you link (`c/binpazer_codec.c`, over zlib/libzstd/liblz4); the core links
nothing but libc, and keeps a test binary that proves it.

All three write byte-identical files; the Go tests verify this against a
C-generated fixture ([`go/testdata/sample.binpazer`](./go/testdata)), and CI
regenerates the fixture from C and `cmp`s it on every push. Compressed blocks
are the deliberate exception: a codec id names a decodable format, not one
encoder, so implementations agree on what a file *means* rather than on the
bytes their compressors emit. CI checks that directly, by having each
implementation decode files the other wrote with every codec.

The readers are hardened against hostile input: every length/count field is
bounded against the real input size (or an explicit cap for non-seekable
streams) *before* anything is allocated, failure modes are typed sentinel
errors (`ErrBadMagic`, `ErrCRCMismatch`, `ErrTruncated`, `ErrCapacityExceeded`
in Go; `BINPAZER_ERR_*` codes in C/C++), and the Go reader is fuzzed.

## The `binpazer` CLI

A command-line tool built on the Go library to author, inspect, validate, and
dissect binpazer files:

```sh
cd go && go-toolchain        # builds go/build/binpazer
```

Prebuilt binaries (linux/darwin/windows x amd64/arm64) are published to
[pazer.build](https://pazer.build) on every push, as project
`bin-file-fmt/binpazer` with the git branch recorded on each release:

```sh
curl -fSsL --location-trusted -H "Authorization: Bearer $BUILDHOST_TOKEN" \
  "https://dl.pazer.build/bin-file-fmt/binpazer?os=linux&arch=amd64" \
  -o binpazer && chmod +x binpazer
```

(add `&branch=<branch>` for a branch tip; `--location-trusted` keeps the
auth header across the redirect to the static download host.)

Any positional file argument accepts `-` for stdin (and `-o -`/`--out -` for
stdout), so the tool composes in pipelines. On non-seekable input, per-field
allocation claims are capped at 1 GiB (tune with `--max-alloc`); seekable input
is bounded by the real file size. `--json` on `info`, `validate`, `dump`, and
`find` emits a single machine-readable JSON object. `binpazer --version`
prints the version.

| Command | What it does |
|---|---|
| `pack` | build a file from a JSON manifest |
| `unpack` | explode a file into payload files + a re-packable manifest |
| `info` | header, block type table, and block listing |
| `validate` | spec-conformance check (the full SPEC.md validation checklist) |
| `dump` | hex-dump block payloads |
| `find` | list blocks of a given type (via the Block Index when present) |
| `extract` (alias `cat`) | stream one block's payload to a file or stdout |

### pack -- build a file from a JSON manifest

```sh
binpazer pack manifest.json -o demo.binpazer
```

```json
{
  "writer_guid": "8f9d2c31-4b6a-4e0f-9a3d-1c2b3a4d5e6f",
  "writer_name": "demo",
  "types": [
    { "type_id": 1, "guid": "6ba7b810-9dad-11d1-80b4-00c04fd430c8", "name": "ExampleBlock" }
  ],
  "blocks": [
    { "type_id": 1, "flags": ["has_crc"], "data": "hello, file!" },
    { "type_id": 1, "file": "payload.bin" }
  ],
  "index": true
}
```

- `writer_guid` / `writer_name` -- the file-header identity.
- `types` -- the Block Type Table entries: `type_id`, `guid`, optional `name`.
- `blocks` -- written in order; each has a `type_id`, optional `flags` (any of
  `critical`, `safe_to_copy`, `has_crc`, `large_mode`), and
  exactly one content source: `data` (UTF-8 text), `data_base64`, or `file`
  (a path relative to the manifest).
- `codec` on a block -- `stored`, `deflate`, `zstd` or `lz4` -- compresses that
  block's payload (SPEC.md "Compression"). The manifest always holds the
  block's LOGICAL bytes, so `unpack` writes decoded payloads and records the
  codec that produced them; re-packing reproduces the file exactly. There is no
  `compressed` flag name: compression is named by its codec or it does not
  happen.
- `"index": true` -- append a Block Index + footer so readers can jump by type.

`pack - -o -` reads the manifest from stdin and writes the archive to stdout
(byte-identical to file output; the status line moves to stderr).

### unpack -- explode a file

```sh
$ binpazer unpack demo.binpazer -d out
unpacked 2 block(s) to out
```

Writes `block<N>.bin` per block plus a `manifest.json`; `pack` on that manifest
reproduces the original file byte-for-byte.

### info -- headers, types, blocks

```sh
$ binpazer info demo.binpazer
binpazer v1.0
writer:      demo (8f9d2c31-4b6a-4e0f-9a3d-1c2b3a4d5e6f)
file_length: 288
has_index:   true

block types (2):
  65534  62696e70-617a-6572-0000-000000000001  BlockTypeTable
  1      6ba7b810-9dad-11d1-80b4-00c04fd430c8  ExampleBlock

blocks:
  [0] type=1      ExampleBlock   flags=[has_crc] offset=168 len=12
  [1] type=1      ExampleBlock   flags=[] offset=192 len=18
  [2] type=65533  BlockIndex     flags=[] offset=224 len=40
```

`--recurse` descends into Group blocks and lists nested blocks indented, with
dotted labels (`[3.1.0]`) and absolute offsets. (On piped input the human
header prints `has_index: false` before the walk can discover the footer;
`info --json` reports it correctly.)

### validate -- spec conformance

```sh
$ binpazer validate demo.binpazer
OK: file is spec-conformant
```

Checks every rule of SPEC.md's Validation section (magic/signature,
first-block-is-BTT, duplicate/invalid/unknown type ids, reserved fields and
flag bits, alignment, `file_length` consistency, string NUL termination and
UTF-8, CRC trailers, footer/Block Index consistency, structural parseability).
Each violation prints as one finding (`rule=... offset=...: detail`) and the
exit status is nonzero when any finding exists. `--json` emits
`{ok, findings, rules}` with a per-rule pass/fail/skipped status; on
non-seekable stdin, rules that need to revisit bytes report as skipped rather
than silently passing. Payloads are streamed, never buffered, so a 1 GiB file
validates in ~3 MB of memory.

### dump -- hex payloads

```sh
$ binpazer dump demo.binpazer --block 0
block 0 (type 1, 12 bytes):
00000000  68 65 6c 6c 6f 2c 20 66  69 6c 65 21              |hello, file!|
```

`--block N` selects one block (out-of-range is an error), `--recurse` descends
into Groups, `--json` swaps the hex for `payload_base64`.

### find / extract -- jump by type

The format's headline feature: with a Block Index + footer, a reader seeks
straight to a block instead of walking the file. `find` lists the matches,
`extract` (alias `cat`) streams one payload -- CRC-verified, and decoded when
the block is compressed -- to `--out` or stdout (`--stored` emits the
Compression Envelope and codec bytes as they sit in the file instead):

```sh
$ binpazer find demo.binpazer --type 1
[0] offset=168 len=12 flags=[has_crc]
[1] offset=192 len=18 flags=[]

$ binpazer extract demo.binpazer --type 1 --index 1 -o payload.bin
```

`find` exits 1 when nothing matches (grep semantics); its printed `[i]` is
`extract`'s `--index`. Seekable inputs jump via the index; pipes fall back to
a sequential walk:

```sh
$ cat demo.binpazer | binpazer cat - --type 1
hello, file!
```

## In brief

- Fixed file header: `magic` = `binpazer`, version, writer GUID + name, file length.
- Each block: an 8-byte header (`type_id` + `flags` + `length`) then payload,
  8-byte aligned. The header casts directly as a C struct.
- Block types are identified globally by **GUID**, interned to small per-file
  `type_id` integers via a required **Block Type Table** (the first block).
- A `critical`/ancillary flag tells readers whether skipping an unknown block is
  safe.
- `length` is a byte count, or -- with the `large_mode` flag -- a count of
  4096-byte (page) units, for payloads over 4 GiB and up to ~16 TiB.
- Strings are length-prefixed **and** null-terminated.
- An optional **Block Index** plus a 16-byte footer let a reader seek straight
  to a block instead of walking it -- random access, which a `.tar.gz` has no
  way to offer.
- Payloads may be **compressed per block** (1.1): a Compression Envelope names
  the codec (`stored`, `deflate`, `zstd`, `lz4`, or a GUID for anything else),
  so one member decodes without touching the others. `length` still counts
  stored bytes, so older readers keep walking such files correctly, and a file
  that compresses nothing is byte-identical to a 1.0 file.
