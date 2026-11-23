# bin-file-fmt

**binpazer** -- a flexible, block-based binary container format. A sequence of self-describing blocks follow a small fixed header. A reader can walk the file and skip blocks it does not understand. The layout is little-endian and 8-byte aligned so a C program can `mmap` the file and cast pointers into it.

See **[SPEC.md](./SPEC.md)** for the format.

## Implementations

Minimal-dependency libraries that do **no file IO and no mmap** -- the caller injects IO via callbacks (C/C++). The caller also libraries `io.Reader`/`io.Writer` (Go), or a `Source`/`Sink` object (TypeScript).

| Dir | Language | IO | Test |
|---|---|---|---|
| [`c/`](./c) | C11, allocation-free | `read`/`write` (+ optional `seek`) function pointers | `make -C c test` |
| [`cpp/`](./cpp) | C++17, header-only convenience wrapper over the C core | `std::function` callbacks | `make -C cpp test` |
| [`go/`](./go) | pure Go library (the CLI uses cobra) | `io.Reader` / `io.Writer` (`io.Seeker` optional) | `go-toolchain` (in `go/`) |
| [`ts/`](./ts) | TypeScript, no npm dependency; the core also runs in a browser | `Source` / `Sink` objects (`seek` optional) | `ts0 test` (in `ts/`) |
| [`py/`](./py) | Python 3.10+, no PyPI dependency | binary file objects (`seek` optional) | `make -C py test` |

Each one implements **every codec the format defines**, so no implementation can write a file another cannot read. In C and C++ the codecs are a second file you link (`c/binpazer_codec.c`, over zlib/libzstd/liblz4). The core links nothing but libc, and keeps a test binary that proves it. TypeScript makes the same split: `ts/src/core.ts` imports nothing, and `ts/src/index.ts` adds the codecs. Python keeps its codecs in `py/binpazer/codec.py`, which loads the system libzstd and liblz4.

All write byte-identical files. The Go, TypeScript and Python tests verify this against a C-generated fixture ([`go/testdata/sample.binpazer`](./go/testdata)). CI regenerates the fixture from C and `cmp`s it on every push. Compressed blocks are the deliberate exception. A codec id names a decodable format, not one encoder. Implementations agree on what a file *means* rather than on the bytes their compressors emit.

CI checks that directly, by having each implementation decode files the other wrote with every codec.

## Benchmarks

[`bench/`](./bench) runs the same workloads through every implementation and compares CPU time and peak memory. [`bench/RESULTS.md`](./bench/RESULTS.md) holds the measured comparison and the hot functions of each implementation. [`bench/README.md`](./bench/README.md) says how to run it.

CPU seconds per operation from that run (user + sys, median of the timed runs, lower is better):

| Workload / operation | C | C++ | Go | TypeScript | Python |
|---|---:|---:|---:|---:|---:|
| small (256 B blocks) / write | 0.044 | 0.049 | 0.075 | 0.296 | 0.523 |
| small / read | 0.033 | 0.043 | 0.101 | 0.723 | 0.862 |
| small / find | 0.200 | 0.194 | 0.416 | 0.865 | 2.858 |
| medium (64 KiB blocks) / write | 0.068 | 0.062 | 0.078 | 0.303 | 0.113 |
| medium / read | 0.028 | 0.030 | 0.112 | 0.294 | 0.107 |
| large (8 MiB blocks) / write | 0.089 | 0.082 | 0.097 | 0.357 | 0.141 |
| large / read | 0.047 | 0.053 | 0.110 | 0.395 | 0.124 |
| deflate / write | 0.916 | 0.883 | 0.878 | 0.937 | 0.998 |
| deflate / read | 0.147 | 0.155 | 0.590 | 0.263 | 0.305 |
| zstd / write | 0.257 | 0.241 | 0.711 | 0.379 | 0.401 |
| zstd / read | 0.020 | 0.025 | 0.366 | 0.172 | 0.089 |
| lz4 / write | 0.063 | 0.069 | 0.316 | 0.662 | 0.185 |
| lz4 / read | 0.013 | 0.017 | 0.331 | 0.201 | 0.092 |

CRC-32C throughput of the C core by CPU, with XXH3 built for the same CPU as a yardstick (GiB/s, 1 MiB buffer, from `make -C c speed` and `make -C c speed-xxh3`):

| CPU | Table CRC | CRC-32C path in use | Its throughput | XXH3 |
|---|---:|---|---:|---:|
| x86-64 with AVX-512 and VPCLMULQDQ | 1.94 | carry-less folding | 64.11 | 53.40 |
| arm64 Linux (GitHub runner) | 2.28 | three CRC streams | 22.91 | 13.41 |
| Apple silicon (GitHub runner) | 1.19 | PMULL folding | 57.21 | 26.23 |

The arm64 rows come from the `c-arm64` CI job, which prints them again on every run. An arm64 Linux CPU of another kind can rank the paths differently, so on arm64 Linux the library times its fastest paths. This happens once and keeps the winner. `make -C c speed` prints every path on any machine.

Every block in these workloads carries a CRC. The C core computes CRC-32C with the fastest instructions the CPU has: AVX-512 carry-less folding on this machine. Python calls the C implementation's CRC-32C and the system codec libraries, so its cost is per block, not per byte. The full report also has peak memory, wall time, throughput and file sizes.

The Go library also ships a convenience layer for the shape most binpazer files take. Content blocks, a directory naming where they landed, then the index -- so building one is not twenty lines of bookkeeping:

```go
off, _ := w.PutCompressed(typeFile, binpazer.FlagHasCRC, binpazer.CodecZstd, body)
dir.Entries[name] = off                       // Put* reports where the block landed
w.PutJSON(typeDir, binpazer.FlagCritical, binpazer.CodecZstd, dir)
w.Finish()                                    // Block Index + footer

r, _ := binpazer.NewReaderAt(blob, size)      // mmap, file, or []byte
r.DecodeJSONLast(typeDir, &dir)               // newest directory wins
body, _, _ := r.OpenAt(dir.Entries[name], typeFile)   // seek + type check + decode
```

It composes the primitives and adds no format feature. These files are ordinary binpazer files that the C and C++ implementations read as-is.

A file can also **grow while it is read**, the way a JSON Lines log does. An appendable file seals every block with a CRC. A reader takes a block only when all of it is there and the seal verifies. A block that a writer is still writing is never seen:

```go
w, _ := binpazer.NewAppendWriter(f, writerGUID, "logger", defs)
w.Block(typeEvent, 0, payload)                // sealed and flushed: committed on return

r, _ := binpazer.NewReader(g)                 // another process, same file
for {
	b, err := r.NextCommitted()
	if errors.Is(err, binpazer.ErrPending) {  // nothing new yet; the reader has not moved
		time.Sleep(interval)
		continue
	}
	if err == io.EOF {                        // the writer called End
		break
	}
	payload, _ := r.ReadPayload(b)
}
```

`ResumeAppendWriter` continues a file after a restart, and seals the torn tail a crash left behind. See SPEC.md "Appendable files".

The readers are hardened against hostile input. `ErrCapacityExceeded` in Go. `BINPAZER_ERR_*` codes in C/C++). The Go reader is fuzzed.

## The `binpazer` CLI

A command-line tool built on the Go library to author, inspect, validate, and dissect binpazer files:

```sh
cd go && go-toolchain        # builds go/build/binpazer
```

Prebuilt binaries (linux/darwin/windows x amd64/arm64) are published to [pazer.build](https://pazer.build) on every push, as project `bin-file-fmt/binpazer` with the git branch recorded on each release:

```sh
curl -fSsL --location-trusted -H "Authorization: Bearer $BUILDHOST_TOKEN" \
  "https://dl.pazer.build/bin-file-fmt/binpazer?os=linux&arch=amd64" \
  -o binpazer && chmod +x binpazer
```

(add `&branch=<branch>` for a branch tip. `--location-trusted` keeps the auth header across the redirect to the static download host.)

Any positional file argument accepts `-` for stdin (and `-o -`/`--out -` for stdout), so the tool composes in pipelines. On non-seekable input, per-field allocation claims are capped at 1 GiB (tune with `--max-alloc`). The real file size bounds seekable input. `--json` emits a single machine-readable JSON object. `binpazer --version` prints the version.

| Command | What it does |
|---|---|
| `pack` | build a file from a JSON manifest |
| `unpack` | explode a file into payload files + a re-packable manifest |
| `info` | header, block type table, and block listing |
| `validate` | spec-conformance check (the full SPEC.md validation checklist) |
| `dump` | hex-dump block payloads |
| `find` | list blocks of a given type (via the Block Index when present) |
| `extract` (alias `cat`) | stream one block's payload to a file or stdout |
| `follow` | print the blocks of an appendable file as a writer commits them |
| `append` | add a block to an open appendable file, repair it, or end it |

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
- `blocks` -- written in order. Each has a `type_id`, optional `flags`, and exactly one content source.
- The manifest always holds the block's LOGICAL bytes, so `unpack` writes decoded payloads and records the codec that produced them. Re-packing reproduces the file exactly. There is no `compressed` flag name: compression is named by its codec or it does not happen.
- `"index": true` -- append a footer of the Block Index + so readers can jump by type.
- `"appendable": true` -- write an appendable file. Add `"open": true` to leave it without its End block, so `append` can continue it.

`pack - -o -` reads the manifest from stdin and writes the archive to stdout (byte-identical to file output. The status line moves to stderr).

### unpack -- explode a file

```sh
$ binpazer unpack demo.binpazer -d out
unpacked 2 block(s) to out
```

Writes `block<N>.bin` per block plus a `manifest.json`. `pack` on that manifest reproduces the original file byte-for-byte.

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

`--recurse` descends into Group blocks and lists nested blocks indented, with dotted labels (`[3.1.0]`) and absolute offsets. (On piped input the human header prints `has_index: false` before the walk can find the footer. `info --json` reports it correctly.)

### validate -- spec conformance

```sh
$ binpazer validate demo.binpazer
OK: file is spec-conformant
```

Checks every rule of SPEC.md's Validation section (magic/signature, first-block-is-BTT, duplicate/invalid/unknown type ids, reserved fields and flag bits, alignment, `file_length` consistency, string NUL termination and UTF-8, CRC trailers, footer/Block Index consistency, structural parseability). Each violation prints as one finding (`rule=... offset=...: detail`) and the exit status is nonzero when any finding exists. `--json` emits `{ok, findings, rules}` with a per-rule pass/fail/skipped status. On non-seekable stdin, rules that need to revisit bytes report as skipped rather than silently passing. Payloads are streamed, not buffered, so a 1 GiB file validates in ~3 MB of memory.

### dump -- hex payloads

```sh
$ binpazer dump demo.binpazer --block 0
block 0 (type 1, 12 bytes):
00000000  68 65 6c 6c 6f 2c 20 66  69 6c 65 21              |hello, file!|
```

`--block N` selects one block (out-of-range is an error), `--recurse` descends into Groups, `--json` swaps the hex for `payload_base64`.

### find / extract -- jump by type

The format's headline feature: with a footer of the Block Index +, a reader seeks straight to a block instead of walking the file. `find` lists the matches, `extract` (alias `cat`) streams one payload -- CRC-verified, and decoded when the block is compressed -- to `--out` or stdout (`--stored` emits the Compression Envelope and codec bytes as they sit in the file instead):

```sh
$ binpazer find demo.binpazer --type 1
[0] offset=168 len=12 flags=[has_crc]
[1] offset=192 len=18 flags=[]

$ binpazer extract demo.binpazer --type 1 --index 1 -o payload.bin
```

`find` exits 1 when nothing matches (grep semantics). Its printed `[i]` is `extract`'s `--index`. Seekable inputs jump via the index. Pipes fall back to a sequential walk:

```sh
$ cat demo.binpazer | binpazer cat - --type 1
hello, file!
```

### follow / append -- a file that grows

`append` adds one sealed block to an open appendable file. `follow` prints each block as a JSON line once it is committed, and waits for more until the writer ends the file:

```sh
$ binpazer follow log.binpazer &
$ binpazer append log.binpazer --type 1 --data "disk at 91%"
{"index":0,"type_id":1,"type_name":"Event","flags":["has_crc"],"offset":192,"payload_len":11,"payload_base64":"ZGlzayBhdCA5MSU="}
$ binpazer append log.binpazer --end
{"index":1,"type_id":65529,"type_name":"End","flags":["has_crc"],"offset":216,"payload_len":0,"payload_base64":""}
```

`follow --no-wait` prints what is committed now and exits. The other read commands also stop at the last committed block of an open file, and `info` and `validate` say where the unfinished bytes start.

If a writer died in the middle of a block, `append` with no other flag seals that torn tail so the file reads cleanly again.

## In brief

- Fixed file header: `magic` = `binpazer`, version, name of the writer GUID +, file length.
- Each block: an 8-byte header (`type_id` + `flags` + `length`) then payload, 8-byte aligned. The header casts directly as a C struct.
- GUID identifies Block types**, interned to small per-file `type_id` integers via a required **Block Type Table** (the first block).
- A `critical`/ancillary flag tells readers whether skipping an unknown block is safe.
- `length` is a byte count, or -- with the `large_mode` flag -- a count of 4096-byte (page) units, for payloads over 4 GiB and up to ~16 TiB.
- Strings are length-prefixed **and** null-terminated.
- An optional **Block Index** plus a 16-byte footer let a reader seek straight to a block instead of walking it. Random access, which a `.tar.gz` has no way to offer.
- Payloads may be **compressed per block** (1.1): a Compression Envelope names the codec, so one member decodes without touching the others. `length` still counts stored bytes, so older readers keep walking such files correctly, and a file that compresses nothing is byte-identical to a 1.0 file.
- A file may be **appendable** (1.2): its length is unknown. A writer keeps adding blocks, and the CRC trailer of each block is its commit seal. A reader can follow the file while it grows and sees whole blocks only. An End block closes it.
