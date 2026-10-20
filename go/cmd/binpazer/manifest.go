package main

import (
	"fmt"
	"strings"

	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// Manifest is the JSON description of a binpazer file used by pack/unpack.
type Manifest struct {
	WriterGUID string          `json:"writer_guid"`
	WriterName string          `json:"writer_name"`
	Types      []ManifestType  `json:"types"`
	Blocks     []ManifestBlock `json:"blocks"`
	Index      bool            `json:"index,omitempty"`
}

// ManifestType is one Block Type Table entry.
type ManifestType struct {
	TypeID uint16 `json:"type_id"`
	GUID   string `json:"guid"`
	Name   string `json:"name,omitempty"`
}

// ManifestBlock is one block; its content comes from exactly one of data,
// data_base64, or file (a path, relative to the manifest, to embed).
type ManifestBlock struct {
	TypeID     uint16   `json:"type_id"`
	Flags      []string `json:"flags,omitempty"`
	Codec      string   `json:"codec,omitempty"`
	Data       string   `json:"data,omitempty"`
	DataBase64 string   `json:"data_base64,omitempty"`
	File       string   `json:"file,omitempty"`
}

// The `compressed` flag is deliberately absent from this table: it is implied by `codec`.
var flagOrder = []string{"critical", "safe_to_copy", "has_crc", "large_mode"}

var flagBits = map[string]uint16{
	"critical":     binpazer.FlagCritical,
	"safe_to_copy": binpazer.FlagSafeToCopy,
	"has_crc":      binpazer.FlagHasCRC,
	"large_mode":   binpazer.FlagLargeMode,
}

// codecIDs maps the manifest's codec names to SPEC.md's registry ids.
var codecIDs = map[string]uint16{
	"stored":  binpazer.CodecStored,
	"deflate": binpazer.CodecDeflate,
	"zstd":    binpazer.CodecZstd,
	"lz4":     binpazer.CodecLZ4,
}

// codecName is the reverse of codecIDs. An id outside the registry renders as
// "codec<id>", so a file from a later revision still reports something exact.
func codecName(id uint16) string {
	for n, v := range codecIDs {
		if v == id {
			return n
		}
	}
	if id == binpazer.CodecByGUID {
		return "guid"
	}
	return fmt.Sprintf("codec%d", id)
}

// displayFlagNames is flagNames plus `compressed`, for commands that SHOW a
// block rather than describe it for pack (where compression is carried by
// `codec` instead).
func displayFlagNames(f uint16) []string {
	names := flagNames(f)
	if f&binpazer.FlagCompressed != 0 {
		names = append(names, "compressed")
	}
	return names
}

func parseCodec(name string) (uint16, error) {
	id, ok := codecIDs[strings.ToLower(name)]
	if !ok {
		return 0, fmt.Errorf("unknown codec %q (known: stored, deflate, zstd, lz4)", name)
	}
	return id, nil
}

func parseFlags(names []string) (uint16, error) {
	var f uint16
	for _, n := range names {
		b, ok := flagBits[strings.ToLower(n)]
		if !ok {
			return 0, fmt.Errorf("unknown flag %q", n)
		}
		f |= b
	}
	return f, nil
}

func flagNames(f uint16) []string {
	var out []string
	for _, n := range flagOrder {
		if f&flagBits[n] != 0 {
			out = append(out, n)
		}
	}
	return out
}

// predefinedName returns the human name of a predefined type id, or "".
func predefinedName(id uint16) string {
	switch id {
	case binpazer.TypeBlockTypeTable:
		return "BlockTypeTable"
	case binpazer.TypeBlockIndex:
		return "BlockIndex"
	case binpazer.TypeWriterInfo:
		return "WriterInfo"
	case binpazer.TypePadding:
		return "Padding"
	case binpazer.TypeGroup:
		return "Group"
	}
	return ""
}
