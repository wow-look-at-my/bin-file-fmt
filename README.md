# bin-file-fmt

A flexible, block-based binary file format (**binpazer**) designed to be
walkable without understanding every block (like PNG/RIFF, pushed further) and
to be `mmap`-friendly for zero-copy reads in C.

See **[SPEC.md](./SPEC.md)** for the full format specification. It is a DRAFT /
request-for-comment -- sections marked `[OPEN]` still want a decision and
`[DEVIATION]` marks where the draft diverges from the original notes (with
rationale).

## TL;DR

- Fixed file header (`magic` = `binpazer`, version, writer GUID + name, length).
- A sequence of **blocks**: `type_id` + `flags` + `type_version` + `payload_length`
  + payload, 8-byte aligned.
- Block types are identified globally by **GUID**, interned to small per-file
  `type_id` integers via a required **Block Type Table** (the first block).
- A `critical`/ancillary flag tells readers whether skipping an unknown block is
  safe.
- Strings are length-prefixed **and** null-terminated (navigable + C-mmappable).
