// Package binpazer is a pure-Go implementation of the binpazer block-based
// binary file format. See SPEC.md.
//
// It does no file IO and no mmap: the caller supplies an io.Writer (optionally
// also an io.Seeker, to back-patch the exact file length) or an io.Reader.
package binpazer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

const (
	Magic             = "binpazer"
	VersionMajor      = 1
	VersionMinor      = 0
	FileLengthUnknown = ^uint64(0)

	TypeInvalid        = 0
	TypeBlockTypeTable = 1
)

// Block flag bits.
const (
	FlagCritical   uint16 = 1 << 0
	FlagSafeToCopy uint16 = 1 << 1
	FlagCompressed uint16 = 1 << 2
	FlagHasCRC     uint16 = 1 << 3
	FlagContainer  uint16 = 1 << 4
	FlagLargeMode  uint16 = 1 << 5
)

// GUID is a 16-byte block-type identifier, in canonical (RFC 4122) byte order.
type GUID [16]byte

// BlockTypeTableGUID is the well-known GUID of the Block Type Table.
var BlockTypeTableGUID = GUID{0x62, 0x69, 0x6e, 0x70, 0x61, 0x7a, 0x65, 0x72, 0, 0, 0, 0, 0, 0, 0, 1}

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// TypeDef declares one block type for the Block Type Table. Do not include type
// id 1; the table's own entry is added automatically.
type TypeDef struct {
	TypeID uint16
	GUID   GUID
	Name   string
}

// TypeEntry is a resolved entry from a file's Block Type Table.
type TypeEntry struct {
	TypeID uint16
	GUID   GUID
}

func align8(x uint64) uint64 { return (x + 7) &^ 7 }

func bttEntrySize(nameLen int) uint64 { return align8(20 + 4 + uint64(nameLen) + 1) }

// ---- Writer ----

// Writer serializes a binpazer file to an io.Writer.
type Writer struct {
	w      io.Writer
	seeker io.Seeker // non-nil if w also implements io.Seeker
	off    uint64
	err    error
}

var zeros [8]byte

func (bw *Writer) write(p []byte) {
	if bw.err != nil {
		return
	}
	n, err := bw.w.Write(p)
	if err != nil {
		bw.err = err
		return
	}
	if n != len(p) {
		bw.err = io.ErrShortWrite
		return
	}
	bw.off += uint64(n)
}

func (bw *Writer) pad(n uint64) {
	for n > 0 {
		c := n
		if c > 8 {
			c = 8
		}
		bw.write(zeros[:c])
		n -= c
	}
}

func (bw *Writer) writeBTTEntry(typeID uint16, guid GUID, name string) {
	var hdr [20]byte
	binary.LittleEndian.PutUint16(hdr[0:], typeID)
	// entry_flags (hdr[2:4]) = 0
	copy(hdr[4:20], guid[:])
	bw.write(hdr[:])
	var lp [4]byte
	binary.LittleEndian.PutUint32(lp[:], uint32(len(name)))
	bw.write(lp[:])
	bw.write([]byte(name))
	bw.write([]byte{0})
	raw := uint64(25 + len(name))
	bw.pad(align8(raw) - raw)
}

// NewWriter writes the file header and the Block Type Table, then returns a
// Writer ready for Block calls. If w implements io.Seeker, the exact file length
// is back-patched by End; otherwise the streaming sentinel is used.
func NewWriter(w io.Writer, writerGUID GUID, name string, defs []TypeDef) (*Writer, error) {
	bw := &Writer{w: w}
	if s, ok := w.(io.Seeker); ok {
		bw.seeker = s
	}

	nameLen := uint64(len(name))
	firstBlock := align8(56 + 4 + nameLen + 1)

	var hdr [56]byte
	copy(hdr[0:8], Magic)
	hdr[8], hdr[9], hdr[10], hdr[11] = 0x0D, 0x0A, 0x1A, 0x0A
	binary.LittleEndian.PutUint16(hdr[12:], VersionMajor)
	binary.LittleEndian.PutUint16(hdr[14:], VersionMinor)
	copy(hdr[16:32], writerGUID[:])
	binary.LittleEndian.PutUint64(hdr[32:], FileLengthUnknown)
	binary.LittleEndian.PutUint64(hdr[40:], firstBlock)
	bw.write(hdr[:])

	var lp [4]byte
	binary.LittleEndian.PutUint32(lp[:], uint32(nameLen))
	bw.write(lp[:])
	bw.write([]byte(name))
	bw.write([]byte{0})
	bw.pad(firstBlock - bw.off)

	payloadLen := uint64(8) + bttEntrySize(len("BlockTypeTable"))
	for _, d := range defs {
		payloadLen += bttEntrySize(len(d.Name))
	}
	if payloadLen > 0xFFFFFFFF {
		bw.err = errors.New("binpazer: block type table too large")
		return bw, bw.err
	}

	var bh [8]byte
	binary.LittleEndian.PutUint16(bh[0:], TypeBlockTypeTable)
	binary.LittleEndian.PutUint16(bh[2:], FlagCritical)
	binary.LittleEndian.PutUint32(bh[4:], uint32(payloadLen))
	bw.write(bh[:])

	var cnt [8]byte
	binary.LittleEndian.PutUint32(cnt[0:], uint32(len(defs)+1))
	bw.write(cnt[:])

	bw.writeBTTEntry(TypeBlockTypeTable, BlockTypeTableGUID, "BlockTypeTable")
	for _, d := range defs {
		bw.writeBTTEntry(d.TypeID, d.GUID, d.Name)
	}
	bw.pad(align8(bw.off) - bw.off)
	return bw, bw.err
}

// Block writes one block. If flags has FlagLargeMode, len(payload) must be a
// multiple of 4 (the on-disk length field stores len/4).
func (bw *Writer) Block(typeID, flags uint16, payload []byte) error {
	if bw.err != nil {
		return bw.err
	}
	var lengthField uint32
	if flags&FlagLargeMode != 0 {
		if len(payload)%4 != 0 {
			bw.err = errors.New("binpazer: large_mode payload must be a multiple of 4 bytes")
			return bw.err
		}
		words := uint64(len(payload)) / 4
		if words > 0xFFFFFFFF {
			bw.err = errors.New("binpazer: payload too large")
			return bw.err
		}
		lengthField = uint32(words)
	} else {
		if uint64(len(payload)) > 0xFFFFFFFF {
			bw.err = errors.New("binpazer: payload exceeds 4 GiB (use FlagLargeMode)")
			return bw.err
		}
		lengthField = uint32(len(payload))
	}

	var bh [8]byte
	binary.LittleEndian.PutUint16(bh[0:], typeID)
	binary.LittleEndian.PutUint16(bh[2:], flags)
	binary.LittleEndian.PutUint32(bh[4:], lengthField)
	bw.write(bh[:])
	bw.write(payload)
	if flags&FlagHasCRC != 0 {
		crc := crc32.Update(0, crcTable, bh[:])
		crc = crc32.Update(crc, crcTable, payload)
		var cb [4]byte
		binary.LittleEndian.PutUint32(cb[:], crc)
		bw.write(cb[:])
	}
	bw.pad(align8(bw.off) - bw.off)
	return bw.err
}

// End finishes the file, back-patching file_length if the writer is seekable.
func (bw *Writer) End() error {
	if bw.err != nil {
		return bw.err
	}
	if bw.seeker != nil {
		total := bw.off
		if _, err := bw.seeker.Seek(32, io.SeekStart); err != nil {
			bw.err = err
			return bw.err
		}
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], total)
		if _, err := bw.w.Write(b[:]); err != nil {
			bw.err = err
			return bw.err
		}
	}
	return bw.err
}

// ---- Reader ----

// Reader parses a binpazer file from an io.Reader.
type Reader struct {
	r   io.Reader
	off uint64

	VersionMajor     uint16
	VersionMinor     uint16
	WriterGUID       GUID
	WriterName       string
	FileLength       uint64
	FirstBlockOffset uint64
	Types            []TypeEntry
}

// Block is a parsed block header.
type Block struct {
	TypeID     uint16
	Flags      uint16
	PayloadLen uint64 // bytes, already decoded from large_mode
	GUID       GUID   // resolved from the type table (zero if unknown)
	Known      bool
	payloadOff uint64
}

func (b *Block) nextOffset() uint64 {
	crc := uint64(0)
	if b.Flags&FlagHasCRC != 0 {
		crc = 4
	}
	return align8(b.payloadOff + b.PayloadLen + crc)
}

func (br *Reader) read(p []byte) error {
	if _, err := io.ReadFull(br.r, p); err != nil {
		return err
	}
	br.off += uint64(len(p))
	return nil
}

func (br *Reader) skip(n uint64) error {
	if n == 0 {
		return nil
	}
	m, err := io.CopyN(io.Discard, br.r, int64(n))
	br.off += uint64(m)
	return err
}

// NewReader reads and validates the file header and Block Type Table.
func NewReader(r io.Reader) (*Reader, error) {
	br := &Reader{r: r}

	hdr := make([]byte, 56)
	if err := br.read(hdr); err != nil {
		return br, err
	}
	if string(hdr[0:8]) != Magic {
		return br, errors.New("binpazer: bad magic")
	}
	if hdr[8] != 0x0D || hdr[9] != 0x0A || hdr[10] != 0x1A || hdr[11] != 0x0A {
		return br, errors.New("binpazer: bad signature")
	}
	br.VersionMajor = binary.LittleEndian.Uint16(hdr[12:])
	br.VersionMinor = binary.LittleEndian.Uint16(hdr[14:])
	if br.VersionMajor > VersionMajor {
		return br, fmt.Errorf("binpazer: unsupported major version %d", br.VersionMajor)
	}
	copy(br.WriterGUID[:], hdr[16:32])
	br.FileLength = binary.LittleEndian.Uint64(hdr[32:])
	br.FirstBlockOffset = binary.LittleEndian.Uint64(hdr[40:])

	lp := make([]byte, 4)
	if err := br.read(lp); err != nil {
		return br, err
	}
	nameLen := uint64(binary.LittleEndian.Uint32(lp))
	name := make([]byte, nameLen)
	if err := br.read(name); err != nil {
		return br, err
	}
	br.WriterName = string(name)
	if err := br.skip(1); err != nil { // trailing NUL
		return br, err
	}
	if br.off > br.FirstBlockOffset {
		return br, errors.New("binpazer: header overruns first block offset")
	}
	if err := br.skip(br.FirstBlockOffset - br.off); err != nil {
		return br, err
	}

	bh := make([]byte, 8)
	if err := br.read(bh); err != nil {
		return br, err
	}
	if binary.LittleEndian.Uint16(bh) != TypeBlockTypeTable {
		return br, errors.New("binpazer: first block is not the block type table")
	}
	bttLen := uint64(binary.LittleEndian.Uint32(bh[4:]))

	cc := make([]byte, 8)
	if err := br.read(cc); err != nil {
		return br, err
	}
	entryCount := binary.LittleEndian.Uint32(cc)
	br.Types = make([]TypeEntry, 0, entryCount)
	for i := uint32(0); i < entryCount; i++ {
		eh := make([]byte, 20)
		if err := br.read(eh); err != nil {
			return br, err
		}
		var e TypeEntry
		e.TypeID = binary.LittleEndian.Uint16(eh)
		copy(e.GUID[:], eh[4:20])
		lp2 := make([]byte, 4)
		if err := br.read(lp2); err != nil {
			return br, err
		}
		nlen := uint64(binary.LittleEndian.Uint32(lp2))
		if err := br.skip(nlen + 1); err != nil { // name + NUL
			return br, err
		}
		raw := 25 + nlen
		if err := br.skip(align8(raw) - raw); err != nil { // entry pad
			return br, err
		}
		br.Types = append(br.Types, e)
	}
	bttEnd := align8(br.FirstBlockOffset + 8 + bttLen)
	if br.off > bttEnd {
		return br, errors.New("binpazer: block type table overrun")
	}
	if err := br.skip(bttEnd - br.off); err != nil {
		return br, err
	}
	return br, nil
}

// Next reads the next block header, resolving its GUID against the type table.
// It returns io.EOF after the last block. The payload is not read.
func (br *Reader) Next() (*Block, error) {
	if br.FileLength != FileLengthUnknown && br.off >= br.FileLength {
		return nil, io.EOF
	}
	bh := make([]byte, 8)
	n, err := io.ReadFull(br.r, bh)
	if n == 0 && err == io.EOF {
		return nil, io.EOF
	}
	if err != nil {
		return nil, err
	}
	br.off += 8

	b := &Block{}
	b.TypeID = binary.LittleEndian.Uint16(bh)
	b.Flags = binary.LittleEndian.Uint16(bh[2:])
	lf := uint64(binary.LittleEndian.Uint32(bh[4:]))
	if b.Flags&FlagLargeMode != 0 {
		b.PayloadLen = lf * 4
	} else {
		b.PayloadLen = lf
	}
	b.payloadOff = br.off
	for _, e := range br.Types {
		if e.TypeID == b.TypeID {
			b.GUID = e.GUID
			b.Known = true
			break
		}
	}
	return b, nil
}

// ReadPayload reads the current block's payload (verifying the CRC if present)
// and advances to the next block.
func (br *Reader) ReadPayload(b *Block) ([]byte, error) {
	buf := make([]byte, b.PayloadLen)
	if err := br.read(buf); err != nil {
		return nil, err
	}
	if b.Flags&FlagHasCRC != 0 {
		cb := make([]byte, 4)
		if err := br.read(cb); err != nil {
			return nil, err
		}
		var bh [8]byte
		var lf uint32
		if b.Flags&FlagLargeMode != 0 {
			lf = uint32(b.PayloadLen / 4)
		} else {
			lf = uint32(b.PayloadLen)
		}
		binary.LittleEndian.PutUint16(bh[0:], b.TypeID)
		binary.LittleEndian.PutUint16(bh[2:], b.Flags)
		binary.LittleEndian.PutUint32(bh[4:], lf)
		crc := crc32.Update(0, crcTable, bh[:])
		crc = crc32.Update(crc, crcTable, buf)
		if crc != binary.LittleEndian.Uint32(cb) {
			return nil, errors.New("binpazer: CRC mismatch")
		}
	}
	if err := br.skip(b.nextOffset() - br.off); err != nil {
		return nil, err
	}
	return buf, nil
}

// SkipPayload skips the current block's payload (and CRC) and advances.
func (br *Reader) SkipPayload(b *Block) error {
	return br.skip(b.nextOffset() - br.off)
}
