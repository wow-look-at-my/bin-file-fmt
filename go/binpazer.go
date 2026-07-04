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

// ---- Writer ----

// Writer serializes a binpazer file to an io.Writer.
type Writer struct {
	w           io.Writer
	seeker      io.Seeker // non-nil if w also implements io.Seeker
	off         uint64
	err         error
	idx         []IndexEntry // recorded as blocks are written
	indexOffset uint64
	wroteIndex  bool
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
// multiple of 4096 (the on-disk length field stores len/4096).
func (bw *Writer) Block(typeID, flags uint16, payload []byte) error {
	if bw.err != nil {
		return bw.err
	}
	var lengthField uint32
	if flags&FlagLargeMode != 0 {
		if len(payload)%LargeUnit != 0 {
			bw.err = errors.New("binpazer: large_mode payload must be a multiple of 4096 bytes")
			return bw.err
		}
		units := uint64(len(payload)) / LargeUnit
		if units > 0xFFFFFFFF {
			bw.err = errors.New("binpazer: payload too large")
			return bw.err
		}
		lengthField = uint32(units)
	} else {
		if uint64(len(payload)) > 0xFFFFFFFF {
			bw.err = errors.New("binpazer: payload exceeds 4 GiB (use FlagLargeMode)")
			return bw.err
		}
		lengthField = uint32(len(payload))
	}

	bw.idx = append(bw.idx, IndexEntry{TypeID: typeID, Offset: bw.off})

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

// Offset returns the number of bytes written so far, i.e. the file offset where
// the next block will start. Record it before Block to index that block.
func (bw *Writer) Offset() uint64 { return bw.off }

// WriteIndex writes a Block Index covering every block written so far. Call it
// before End, which then emits the footer that locates it.
func (bw *Writer) WriteIndex() error {
	if bw.err != nil {
		return bw.err
	}
	payloadLen := uint64(8) + uint64(len(bw.idx))*16
	if payloadLen > 0xFFFFFFFF {
		bw.err = errors.New("binpazer: block index too large")
		return bw.err
	}
	bw.indexOffset = bw.off

	var bh [8]byte
	binary.LittleEndian.PutUint16(bh[0:], TypeBlockIndex)
	binary.LittleEndian.PutUint16(bh[2:], 0) // ancillary; not safe_to_copy
	binary.LittleEndian.PutUint32(bh[4:], uint32(payloadLen))
	bw.write(bh[:])

	var cnt [8]byte
	binary.LittleEndian.PutUint32(cnt[0:], uint32(len(bw.idx)))
	bw.write(cnt[:])

	for _, e := range bw.idx {
		var eb [16]byte
		binary.LittleEndian.PutUint16(eb[0:], e.TypeID)
		binary.LittleEndian.PutUint64(eb[8:], e.Offset)
		bw.write(eb[:])
	}
	bw.pad(align8(bw.off) - bw.off)
	bw.wroteIndex = true
	return bw.err
}

// End finishes the file: it writes the footer (if an index was written), then
// back-patches file_length when the writer is seekable.
func (bw *Writer) End() error {
	if bw.err != nil {
		return bw.err
	}
	if bw.wroteIndex {
		var ft [16]byte
		binary.LittleEndian.PutUint64(ft[0:], bw.indexOffset)
		copy(ft[8:], footerMagic)
		bw.write(ft[:])
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
	r      io.Reader
	seeker io.Seeker // non-nil if r also implements io.Seeker
	off    uint64

	VersionMajor     uint16
	VersionMinor     uint16
	WriterGUID       GUID
	WriterName       string
	FileLength       uint64
	FirstBlockOffset uint64
	Types            []TypeEntry

	hasIndex    bool
	indexOffset uint64
	blocksEnd   uint64 // end of the block stream (excludes a trailing footer)
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

// Offset returns the block's absolute file offset (the start of its 8-byte header).
func (b *Block) Offset() uint64 { return b.payloadOff - 8 }

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
	if s, ok := r.(io.Seeker); ok {
		br.seeker = s
	}

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
		if nlen > 0 {
			nb := make([]byte, nlen)
			if err := br.read(nb); err != nil {
				return br, err
			}
			e.Name = string(nb)
		}
		if err := br.skip(1); err != nil { // trailing NUL
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

	// Opportunistically locate a Block Index via the footer.
	if br.seeker != nil && br.FileLength != FileLengthUnknown && br.FileLength >= 16 {
		saved := br.off
		if _, err := br.seeker.Seek(int64(br.FileLength-16), io.SeekStart); err == nil {
			ft := make([]byte, 16)
			if _, err := io.ReadFull(br.r, ft); err == nil && string(ft[8:]) == footerMagic {
				br.indexOffset = binary.LittleEndian.Uint64(ft[0:])
				br.hasIndex = true
			}
		}
		if _, err := br.seeker.Seek(int64(saved), io.SeekStart); err != nil {
			return br, err
		}
		br.off = saved
	}
	br.blocksEnd = br.FileLength
	if br.hasIndex {
		br.blocksEnd = br.FileLength - 16 // exclude the trailing footer
	}
	return br, nil
}

// Next reads the next block header, resolving its GUID against the type table.
// It returns io.EOF after the last block. The payload is not read.
func (br *Reader) Next() (*Block, error) {
	if br.blocksEnd != FileLengthUnknown && br.off >= br.blocksEnd {
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
		b.PayloadLen = lf * LargeUnit
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
			lf = uint32(b.PayloadLen / LargeUnit)
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

// HasIndex reports whether a Block Index was located via the footer.
func (br *Reader) HasIndex() bool { return br.hasIndex }

// Offset returns the reader's current file offset.
func (br *Reader) Offset() uint64 { return br.off }

// Find returns the file offsets of all blocks with the given type id. It uses
// the Block Index when present (seeking straight to it) and otherwise walks the
// file. It requires a seekable reader and preserves the reader's position.
func (br *Reader) Find(typeID uint16) ([]uint64, error) {
	if br.seeker == nil {
		return nil, errors.New("binpazer: Find requires a seekable reader")
	}
	saved := br.off
	offsets, ferr := br.find(typeID)
	if _, err := br.seeker.Seek(int64(saved), io.SeekStart); err == nil {
		br.off = saved
	}
	if ferr != nil {
		return nil, ferr
	}
	return offsets, nil
}

func (br *Reader) find(typeID uint16) ([]uint64, error) {
	var offsets []uint64
	if br.hasIndex {
		if _, err := br.seeker.Seek(int64(br.indexOffset), io.SeekStart); err != nil {
			return nil, err
		}
		br.off = br.indexOffset
		bh := make([]byte, 8)
		cc := make([]byte, 8)
		if err := br.read(bh); err != nil {
			return nil, err
		}
		if err := br.read(cc); err != nil {
			return nil, err
		}
		entryCount := binary.LittleEndian.Uint32(cc)
		e := make([]byte, 16)
		for i := uint32(0); i < entryCount; i++ {
			if err := br.read(e); err != nil {
				return nil, err
			}
			if binary.LittleEndian.Uint16(e) == typeID {
				offsets = append(offsets, binary.LittleEndian.Uint64(e[8:]))
			}
		}
		return offsets, nil
	}

	// No index -- walk the file.
	if _, err := br.seeker.Seek(int64(br.FirstBlockOffset), io.SeekStart); err != nil {
		return nil, err
	}
	br.off = br.FirstBlockOffset
	for br.blocksEnd == FileLengthUnknown || br.off < br.blocksEnd {
		blkOff := br.off
		bh := make([]byte, 8)
		n, err := io.ReadFull(br.r, bh)
		if n == 0 && err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		br.off += 8
		tid := binary.LittleEndian.Uint16(bh)
		flags := binary.LittleEndian.Uint16(bh[2:])
		lf := uint64(binary.LittleEndian.Uint32(bh[4:]))
		plen := lf
		if flags&FlagLargeMode != 0 {
			plen = lf * LargeUnit
		}
		if tid == typeID {
			offsets = append(offsets, blkOff)
		}
		crc := uint64(0)
		if flags&FlagHasCRC != 0 {
			crc = 4
		}
		next := align8(blkOff + 8 + plen + crc)
		if _, err := br.seeker.Seek(int64(next), io.SeekStart); err != nil {
			return nil, err
		}
		br.off = next
	}
	return offsets, nil
}

// At seeks to a block (e.g. an offset from Find) and reads its header,
// resolving its GUID against the type table. Requires a seekable reader.
// Afterwards, ReadPayload reads that block.
func (br *Reader) At(offset uint64) (*Block, error) {
	if br.seeker == nil {
		return nil, errors.New("binpazer: At requires a seekable reader")
	}
	if _, err := br.seeker.Seek(int64(offset), io.SeekStart); err != nil {
		return nil, err
	}
	br.off = offset
	bh := make([]byte, 8)
	if err := br.read(bh); err != nil {
		return nil, err
	}
	b := &Block{}
	b.TypeID = binary.LittleEndian.Uint16(bh)
	b.Flags = binary.LittleEndian.Uint16(bh[2:])
	lf := uint64(binary.LittleEndian.Uint32(bh[4:]))
	if b.Flags&FlagLargeMode != 0 {
		b.PayloadLen = lf * LargeUnit
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
