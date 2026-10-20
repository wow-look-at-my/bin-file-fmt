package binpazer

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

// GroupChild is one nested block parsed from a Group block's payload (see
// SPEC.md "Group blocks (nesting)").
type GroupChild struct {
	TypeID  uint16
	Flags   uint16
	Offset  uint64 // of the child's 8-byte header, relative to the payload start
	Payload []byte // aliases the parsed slice
	CRCOK   bool   // false only when FlagHasCRC is set and the stored CRC mismatches
}

// ParseGroupPayload parses a Group block's payload as a sequence of nested
// blocks: 8-byte-aligned block headers laid out exactly like the top level.
// Children parsed before a structural error are returned alongside it, so a
// caller can render the intact prefix of a damaged group. A child's CRC
// (when FlagHasCRC is set) is verified into CRCOK without failing the parse.
func ParseGroupPayload(p []byte) ([]GroupChild, error) {
	var out []GroupChild
	off := uint64(0)
	total := uint64(len(p))
	for off < total {
		rem := total - off
		if rem < 8 {
			if allZero(p[off:]) {
				break // trailing alignment padding
			}
			return out, fmt.Errorf("binpazer: group payload: %d trailing byte(s) at offset %d are not a block: %w",
				rem, off, ErrTruncated)
		}
		var c GroupChild
		c.Offset = off
		c.TypeID = binary.LittleEndian.Uint16(p[off:])
		c.Flags = binary.LittleEndian.Uint16(p[off+2:])
		plen := decodePayloadLen(uint64(binary.LittleEndian.Uint32(p[off+4:])), c.Flags)
		claim := payloadClaim(plen, c.Flags)
		if claim > rem-8 {
			return out, fmt.Errorf("binpazer: group child at offset %d claims %d bytes but only %d remain in the group: %w",
				off, claim, rem-8, ErrTruncated)
		}
		c.Payload = p[off+8 : off+8+plen]
		c.CRCOK = true
		if c.Flags&FlagHasCRC != 0 {
			b := Block{TypeID: c.TypeID, Flags: c.Flags, PayloadLen: plen}
			hb := b.headerBytes()
			crc := crc32.Update(0, crcTable, hb[:])
			crc = crc32.Update(crc, crcTable, c.Payload)
			c.CRCOK = crc == binary.LittleEndian.Uint32(p[off+8+plen:])
		}
		out = append(out, c)
		next := align8(off + 8 + claim)
		if next > total {
			break // final alignment padding not materialized; tolerate
		}
		off = next
	}
	return out, nil
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
