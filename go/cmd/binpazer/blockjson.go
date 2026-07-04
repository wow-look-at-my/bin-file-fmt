package main

import (
	"encoding/base64"
	"fmt"

	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// The machine-readable shapes shared by info --json and dump --json. Every
// command emits exactly one JSON object; nesting appears as children arrays.

// typeJSON is one Block Type Table entry.
type typeJSON struct {
	TypeID uint16 `json:"type_id"`
	GUID   string `json:"guid"`
	Name   string `json:"name,omitempty"`
}

// blockJSON is one block. Index counts within the parent (top level or a
// group); children of groups appear nested under children with --recurse.
// payload_base64 is set by dump only; crc_ok only for group children that
// carry a CRC; error reports a malformed subtree.
type blockJSON struct {
	Index      int         `json:"index"`
	TypeID     uint16      `json:"type_id"`
	TypeName   string      `json:"type_name,omitempty"`
	GUID       string      `json:"guid,omitempty"`
	Flags      []string    `json:"flags,omitempty"`
	Offset     uint64      `json:"offset"`
	PayloadLen uint64      `json:"payload_len"`
	PayloadB64 string      `json:"payload_base64,omitempty"`
	CRCOK      *bool       `json:"crc_ok,omitempty"`
	Children   []blockJSON `json:"children,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// infoJSON is the shape of info --json.
type infoJSON struct {
	VersionMajor uint16      `json:"version_major"`
	VersionMinor uint16      `json:"version_minor"`
	WriterName   string      `json:"writer_name"`
	WriterGUID   string      `json:"writer_guid"`
	FileLength   *uint64     `json:"file_length,omitempty"` // omitted for the streaming sentinel
	HasIndex     bool        `json:"has_index"`
	Types        []typeJSON  `json:"types"`
	Blocks       []blockJSON `json:"blocks"`
}

// dumpJSON is the shape of dump --json.
type dumpJSON struct {
	Blocks []blockJSON `json:"blocks"`
}

// topBlockJSON converts a walked top-level block.
func topBlockJSON(tt *typeTable, b *binpazer.Block, n int) blockJSON {
	bj := blockJSON{
		Index:      n,
		TypeID:     b.TypeID,
		TypeName:   tt.name(b.TypeID),
		Flags:      flagNames(b.Flags),
		Offset:     b.Offset(),
		PayloadLen: b.PayloadLen,
	}
	if b.Known {
		bj.GUID = b.GUID.String()
	}
	return bj
}

// childrenJSON converts a group payload into nested blockJSON children,
// recursing into nested groups up to maxNestDepth. A malformed tail (or the
// depth limit) is returned as the parent's error string; the intact children
// are still returned. withPayload embeds leaf payloads as base64 (dump).
func childrenJSON(tt *typeTable, payload []byte, base uint64, depth int, withPayload bool) ([]blockJSON, string) {
	if depth > maxNestDepth {
		return nil, fmt.Sprintf("nesting depth exceeds %d; not descending", maxNestDepth)
	}
	children, perr := binpazer.ParseGroupPayload(payload)
	out := make([]blockJSON, 0, len(children))
	for i, c := range children {
		bj := blockJSON{
			Index:      i,
			TypeID:     c.TypeID,
			TypeName:   tt.name(c.TypeID),
			Flags:      flagNames(c.Flags),
			Offset:     base + c.Offset,
			PayloadLen: uint64(len(c.Payload)),
		}
		if g, ok := tt.guids[c.TypeID]; ok {
			bj.GUID = g.String()
		}
		if c.Flags&binpazer.FlagHasCRC != 0 {
			ok := c.CRCOK
			bj.CRCOK = &ok
		}
		if tt.isGroup(c.TypeID) {
			bj.Children, bj.Error = childrenJSON(tt, c.Payload, base+c.Offset+8, depth+1, withPayload)
		} else if withPayload {
			bj.PayloadB64 = base64.StdEncoding.EncodeToString(c.Payload)
		}
		out = append(out, bj)
	}
	var errStr string
	if perr != nil {
		errStr = perr.Error()
	}
	return out, errStr
}
