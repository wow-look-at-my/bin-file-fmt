// Package binpazer is a pure-Go implementation of the binpazer block-based
// binary file format. See SPEC.md.
//
// It does no file IO and no mmap: the caller supplies an io.Writer (optionally
// also an io.Seeker, to back-patch the exact file length) or an io.Reader.
//
// The reader defends against hostile inputs: every length and count field is
// bounds-checked against the real input size (learned by seeking) -- or, for
// non-seekable inputs, against a configurable cap (see NewReaderLimited) --
// before any allocation is sized from it. Failures are reported through the
// sentinel errors ErrBadMagic, ErrCRCMismatch, ErrTruncated, and
// ErrCapacityExceeded (match with errors.Is).
package binpazer

import "hash/crc32"

const (
	Magic             = "binpazer"
	VersionMajor      = 1
	VersionMinor      = 0
	FileLengthUnknown = ^uint64(0)

	TypeInvalid        = 0
	TypeInvalidMax     = 65535
	TypeBlockTypeTable = 65534
	TypeBlockIndex     = 65533
	TypeWriterInfo     = 65532
	TypePadding        = 65531
	TypeGroup          = 65530
)

// Block flag bits.
const (
	FlagCritical   uint16 = 1 << 0
	FlagSafeToCopy uint16 = 1 << 1
	FlagCompressed uint16 = 1 << 2
	FlagHasCRC     uint16 = 1 << 3
	FlagLargeMode  uint16 = 1 << 4
)

// LargeUnit is the byte size of one large_mode length unit (one page). With a
// u32 length field this raises the per-block cap to (2^32-1)*4096 (~16 TiB).
const LargeUnit = 4096

// GUID is a 16-byte block-type identifier, in canonical (RFC 4122) byte order.
type GUID [16]byte

// BlockTypeTableGUID is the well-known GUID of the Block Type Table.
var BlockTypeTableGUID = GUID{0x62, 0x69, 0x6e, 0x70, 0x61, 0x7a, 0x65, 0x72, 0, 0, 0, 0, 0, 0, 0, 1}

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// TypeDef declares one block type for the Block Type Table. Do not include type
// id 65534; the table's own entry is added automatically.
type TypeDef struct {
	TypeID uint16
	GUID   GUID
	Name   string
}

// TypeEntry is a resolved entry from a file's Block Type Table.
type TypeEntry struct {
	TypeID uint16
	GUID   GUID
	Name   string
}

// footerMagic marks the optional 16-byte footer that locates the Block Index.
const footerMagic = "rezapnib"

// IndexEntry maps a block's type id to its absolute file offset (Block Index).
type IndexEntry struct {
	TypeID uint16
	Offset uint64
}

func align8(x uint64) uint64 { return (x + 7) &^ 7 }

func bttEntrySize(nameLen int) uint64 { return align8(20 + 4 + uint64(nameLen) + 1) }
