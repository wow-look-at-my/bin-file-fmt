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
| [`cpp/`](./cpp) | C++17, header-only RAII over the C core | `std::function` callbacks | `make -C cpp test` |
| [`go/`](./go) | pure Go, stdlib only | `io.Reader` / `io.Writer` (`io.Seeker` optional) | `go-toolchain` (in `go/`) |

All three write byte-identical files; the Go tests verify this against a
C-generated fixture ([`go/testdata/sample.binpazer`](./go/testdata)).

## In brief

- Fixed file header: `magic` = `binpazer`, version, writer GUID + name, file length.
- Each block: an 8-byte header (`type_id` + `flags` + `length`) then payload,
  8-byte aligned. The header casts directly as a C struct.
- Block types are identified globally by **GUID**, interned to small per-file
  `type_id` integers via a required **Block Type Table** (the first block).
- A `critical`/ancillary flag tells readers whether skipping an unknown block is
  safe.
- `length` is a byte count, or -- with the `large_mode` flag -- a count of 4-byte
  words (for payloads over 4 GiB).
- Strings are length-prefixed **and** null-terminated.
