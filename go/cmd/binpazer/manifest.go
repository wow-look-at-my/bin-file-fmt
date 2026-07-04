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
	Data       string   `json:"data,omitempty"`
	DataBase64 string   `json:"data_base64,omitempty"`
	File       string   `json:"file,omitempty"`
}

var flagOrder = []string{"critical", "safe_to_copy", "has_crc", "large_mode"}

var flagBits = map[string]uint16{
	"critical":     binpazer.FlagCritical,
	"safe_to_copy": binpazer.FlagSafeToCopy,
	"has_crc":      binpazer.FlagHasCRC,
	"large_mode":   binpazer.FlagLargeMode,
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
