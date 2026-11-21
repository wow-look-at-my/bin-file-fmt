// validate_raw.go holds rawStructuralChecks.

package main

import (
	"bufio"
	"encoding/binary"
	"io"

	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

func pad8(n uint64) uint64 { return ((n + 7) &^ 7) - n }

// rawStructuralChecks re-reads the header and Block Type Table regions to
// inspect bytes the library reader deliberately ignores: reserved fields
// (header_flags, header reserved, BTT reserved, entry_flags) and string NUL
// terminators. It runs only after a successful parse, so every length is
// already known to fit the input. It restores the input position so the
// block walk that follows is unaffected. Seekable inputs only.
func (v *validator) rawStructuralChecks(rs io.ReadSeeker, br *binpazer.Reader) {
	cur, err := rs.Seek(0, io.SeekCurrent)
	if err != nil {
		v.add(ruleStructure, 0, "raw header checks: %v", err)
		return
	}
	defer rs.Seek(cur, io.SeekStart) //nolint:errcheck // best-effort restore

	fail := func(err error) { v.add(ruleStructure, 0, "raw header checks: %v", err) }

	hdr := make([]byte, 56)
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		fail(err)
		return
	}
	if _, err := io.ReadFull(rs, hdr); err != nil {
		fail(err)
		return
	}
	if hf := binary.LittleEndian.Uint32(hdr[48:]) &^ binpazer.HeaderFlagAppendable; hf != 0 {
		v.add(ruleReservedZero, 48, "header_flags sets reserved bits %#x", hf)
	}
	if rv := binary.LittleEndian.Uint32(hdr[52:]); rv != 0 {
		v.add(ruleReservedZero, 52, "header reserved field = %#x (must be 0)", rv)
	}

	// writer_name terminator: byte_length bytes must be followed by 0x00.
	nulOff := uint64(60 + len(br.WriterName))
	var nb [1]byte
	if _, err := rs.Seek(int64(nulOff), io.SeekStart); err != nil {
		fail(err)
		return
	}
	if _, err := io.ReadFull(rs, nb[:]); err != nil {
		fail(err)
		return
	}
	if nb[0] != 0 {
		v.add(ruleStringTerm, nulOff, "writer_name terminator is %#02x, not NUL", nb[0])
	}

	// Block Type Table: reserved u32 after entry_count, then per entry the entry_flags and the name terminator.
	base := br.FirstBlockOffset
	if _, err := rs.Seek(int64(base+12), io.SeekStart); err != nil {
		fail(err)
		return
	}
	rr := bufio.NewReader(rs)
	var res [4]byte
	if _, err := io.ReadFull(rr, res[:]); err != nil {
		fail(err)
		return
	}
	if rv := binary.LittleEndian.Uint32(res[:]); rv != 0 {
		v.add(ruleReservedZero, base+12, "block type table reserved field = %#x (must be 0)", rv)
	}
	entryOff := base + 16
	for i := range br.Types {
		var eh [24]byte // 20-byte entry header + 4-byte name length
		if _, err := io.ReadFull(rr, eh[:]); err != nil {
			fail(err)
			return
		}
		if ef := binary.LittleEndian.Uint16(eh[2:]); ef != 0 {
			v.add(ruleReservedZero, entryOff+2, "type table entry %d entry_flags = %#x (must be 0)", i, ef)
		}
		nlen := uint64(binary.LittleEndian.Uint32(eh[20:]))
		if _, err := io.CopyN(io.Discard, rr, int64(nlen)); err != nil {
			fail(err)
			return
		}
		term, err := rr.ReadByte()
		if err != nil {
			fail(err)
			return
		}
		if term != 0 {
			v.add(ruleStringTerm, entryOff+24+nlen,
				"type table entry %d (%q) name terminator is %#02x, not NUL", i, br.Types[i].Name, term)
		}
		raw := 25 + nlen
		if _, err := io.CopyN(io.Discard, rr, int64(pad8(raw))); err != nil {
			fail(err)
			return
		}
		entryOff += raw + pad8(raw)
	}
}

// indexChecks verifies footer/index consistency: the footer points at a real
// Block Index block, the index's reserved fields are zero, and every entry
// points at a real, aligned block of the claimed type. Seekable inputs only
// (a stream cannot revisit the indexed offsets).
func (v *validator) indexChecks(br *binpazer.Reader, indexBlockSeen bool) {
	idxOff, has := br.IndexOffset()
	if !has {
		if indexBlockSeen {
			v.add(ruleIndex, 0, "a Block Index block exists but no footer locates it")
		}
		return
	}
	b, err := br.At(idxOff)
	if err != nil {
		v.add(ruleIndex, idxOff, "footer points at offset %d but no block parses there: %v", idxOff, err)
		return
	}
	if b.TypeID != binpazer.TypeBlockIndex {
		v.add(ruleIndex, idxOff, "footer points at a block of type %d, not the Block Index (%d)",
			b.TypeID, binpazer.TypeBlockIndex)
		return
	}
	payload, err := br.ReadPayload(b)
	if err != nil {
		v.add(ruleIndex, idxOff, "reading the Block Index payload: %v", err)
		return
	}
	if len(payload) < 8 {
		v.add(ruleIndex, idxOff, "Block Index payload is %d bytes; the 8-byte head does not fit", len(payload))
		return
	}
	count := binary.LittleEndian.Uint32(payload)
	if rv := binary.LittleEndian.Uint32(payload[4:]); rv != 0 {
		v.add(ruleReservedZero, idxOff+12, "Block Index reserved field = %#x (must be 0)", rv)
	}
	if uint64(len(payload)) != 8+uint64(count)*16 {
		v.add(ruleIndex, idxOff, "Block Index claims %d entries but its payload is %d bytes (want %d)",
			count, len(payload), 8+uint64(count)*16)
		return
	}
	for i := uint32(0); i < count; i++ {
		e := payload[8+i*16:]
		eOff := idxOff + 16 + uint64(i)*16 // absolute offset of this entry
		tid := binary.LittleEndian.Uint16(e)
		if rv := binary.LittleEndian.Uint16(e[2:]); rv != 0 {
			v.add(ruleReservedZero, eOff+2, "index entry %d reserved u16 = %#x (must be 0)", i, rv)
		}
		if rv := binary.LittleEndian.Uint32(e[4:]); rv != 0 {
			v.add(ruleReservedZero, eOff+4, "index entry %d reserved u32 = %#x (must be 0)", i, rv)
		}
		target := binary.LittleEndian.Uint64(e[8:])
		if target%8 != 0 {
			v.add(ruleAlignment, target, "index entry %d (type %d) points at unaligned offset %d", i, tid, target)
			continue
		}
		tb, err := br.At(target)
		if err != nil {
			v.add(ruleIndex, target, "index entry %d (type %d) points at offset %d but no block parses there: %v",
				i, tid, target, err)
			continue
		}
		if tb.TypeID != tid {
			v.add(ruleIndex, target, "index entry %d claims type %d but the block at offset %d has type %d",
				i, tid, target, tb.TypeID)
		}
	}
}
