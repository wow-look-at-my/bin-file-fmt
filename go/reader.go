package binpazer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
)

// DefaultMaxAlloc is the allocation cap NewReader applies to any single
// length/count field when the total input size cannot be learned (the input is
// not an io.Seeker). Use NewReaderLimited to choose a different cap.
const DefaultMaxAlloc int64 = 1 << 30 // 1 GiB

// minBTTEntrySize is the smallest on-disk Block Type Table entry: a 20-byte
// header, a 4-byte name length, an empty name, and a NUL, padded to 8 bytes.
const minBTTEntrySize = 32

// Reader parses a binpazer file from an io.Reader.
type Reader struct {
	r      io.Reader
	seeker io.Seeker // non-nil if r also implements io.Seeker
	off    uint64

	inputSize int64 // total input size in bytes; -1 when unknown (non-seekable)
	maxAlloc  int64 // per-field allocation cap used when inputSize is unknown

	VersionMajor     uint16
	VersionMinor     uint16
	WriterGUID       GUID
	WriterName       string
	FileLength       uint64
	FirstBlockOffset uint64
	Types            []TypeEntry

	typeIdx map[uint16]int // type_id -> first index in Types, built by readBTT

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

// decodePayloadLen converts the on-disk 32-bit length field to a byte count.
// lf comes from a uint32, so the large-mode multiply cannot overflow uint64:
// (2^32-1) * 4096 < 2^44.
func decodePayloadLen(lf uint64, flags uint16) uint64 {
	if flags&FlagLargeMode != 0 {
		return lf * LargeUnit
	}
	return lf
}

// payloadClaim is the byte cost a block header claims: the payload plus the
// trailing CRC when present (saturating, so a hostile PayloadLen cannot wrap).
func payloadClaim(payloadLen uint64, flags uint16) uint64 {
	if flags&FlagHasCRC != 0 && payloadLen <= math.MaxUint64-4 {
		return payloadLen + 4
	}
	return payloadLen
}

// wrapEOF converts an unexpected end-of-input into an ErrTruncated-wrapped
// error; other errors pass through unchanged.
func (br *Reader) wrapEOF(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("binpazer: unexpected end of input at offset %d: %w", br.off, ErrTruncated)
	}
	return err
}

// claim verifies that a length/count field read from the file can plausibly be
// satisfied, before any allocation is sized from it. When the total input size
// is known the bound is the bytes remaining; otherwise maxAlloc caps it.
func (br *Reader) claim(n uint64, what string) error {
	if br.inputSize >= 0 {
		rem := br.inputSize - int64(br.off)
		if rem < 0 {
			rem = 0
		}
		if n > uint64(rem) {
			return fmt.Errorf("binpazer: %s claims %d bytes but only %d remain in the input: %w", what, n, rem, ErrTruncated)
		}
		return nil
	}
	if n > uint64(br.maxAlloc) {
		return fmt.Errorf("binpazer: %s claims %d bytes, over the %d-byte cap for non-seekable input: %w", what, n, br.maxAlloc, ErrCapacityExceeded)
	}
	return nil
}

func (br *Reader) read(p []byte) error {
	n, err := io.ReadFull(br.r, p)
	br.off += uint64(n)
	if err != nil {
		return br.wrapEOF(err)
	}
	return nil
}

// skip advances the reader n bytes. Seekable inputs seek instead of reading
// through, bounded by the real input size so a truncated file still surfaces
// ErrTruncated exactly like the read-through path; non-seekable inputs fall
// back to reading and discarding.
func (br *Reader) skip(n uint64) error {
	if n == 0 {
		return nil
	}
	if n > math.MaxInt64 {
		return fmt.Errorf("binpazer: cannot skip %d bytes: %w", n, ErrCapacityExceeded)
	}
	if br.seeker != nil && br.inputSize >= 0 {
		target := br.off + n
		if target > uint64(br.inputSize) {
			// Mirror the read-through path: stop at end of input, then
			// report the truncation.
			if _, err := br.seeker.Seek(br.inputSize, io.SeekStart); err != nil {
				return err
			}
			br.off = uint64(br.inputSize)
			return br.wrapEOF(io.ErrUnexpectedEOF)
		}
		if _, err := br.seeker.Seek(int64(target), io.SeekStart); err != nil {
			return err
		}
		br.off = target
		return nil
	}
	m, err := io.CopyN(io.Discard, br.r, int64(n))
	br.off += uint64(m)
	if err != nil {
		return br.wrapEOF(err)
	}
	return nil
}

// NewReader reads and validates the file header and Block Type Table.
//
// When r implements io.Seeker, the reader learns the real input size and
// rejects any length/count field that promises more bytes than remain, before
// allocating for it. Otherwise every such field is capped at DefaultMaxAlloc;
// use NewReaderLimited to pick a different cap for streaming callers.
func NewReader(r io.Reader) (*Reader, error) {
	return NewReaderLimited(r, DefaultMaxAlloc)
}

// NewReaderLimited is NewReader with an explicit allocation cap for
// non-seekable inputs. Any single length/count field claiming more than
// maxAlloc bytes is rejected with ErrCapacityExceeded when the input size
// cannot be learned by seeking. maxAlloc <= 0 selects DefaultMaxAlloc.
// For seekable inputs the actual input size is the (tighter) bound and
// maxAlloc is not consulted.
func NewReaderLimited(r io.Reader, maxAlloc int64) (*Reader, error) {
	br := &Reader{r: r, inputSize: -1, maxAlloc: maxAlloc}
	if br.maxAlloc <= 0 {
		br.maxAlloc = DefaultMaxAlloc
	}
	if s, ok := r.(io.Seeker); ok {
		br.seeker = s
		// Learn the real input size so every untrusted length/count field can
		// be bounds-checked before it sizes an allocation.
		cur, err := s.Seek(0, io.SeekCurrent)
		if err == nil {
			var end int64
			if end, err = s.Seek(0, io.SeekEnd); err == nil {
				br.inputSize = end
				_, err = s.Seek(cur, io.SeekStart)
			}
		}
		if err != nil {
			return br, fmt.Errorf("binpazer: determining input size: %w", err)
		}
	}

	hdr := make([]byte, 56)
	if err := br.read(hdr); err != nil {
		return br, err
	}
	if string(hdr[0:8]) != Magic {
		return br, ErrBadMagic
	}
	if hdr[8] != 0x0D || hdr[9] != 0x0A || hdr[10] != 0x1A || hdr[11] != 0x0A {
		return br, fmt.Errorf("binpazer: bad signature: %w", ErrBadMagic)
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
	if err := br.claim(nameLen, "writer name"); err != nil {
		return br, err
	}
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

	if err := br.readBTT(); err != nil {
		return br, err
	}

	// Opportunistically locate a Block Index via the footer. The header's
	// file_length is untrusted: only probe when it fits the real input size,
	// so a bogus value degrades to "no footer" instead of a wild seek.
	if br.seeker != nil && br.FileLength != FileLengthUnknown && br.FileLength >= 16 &&
		br.inputSize >= 0 && br.FileLength <= uint64(br.inputSize) {
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

// readBTT parses the Block Type Table (the required first block) into
// br.Types, bounds-checking every count and name length before allocating.
func (br *Reader) readBTT() error {
	bh := make([]byte, 8)
	if err := br.read(bh); err != nil {
		return err
	}
	if binary.LittleEndian.Uint16(bh) != TypeBlockTypeTable {
		return errors.New("binpazer: first block is not the block type table")
	}
	bttLen := uint64(binary.LittleEndian.Uint32(bh[4:]))
	if err := br.claim(bttLen, "block type table"); err != nil {
		return err
	}
	bttEnd := align8(br.FirstBlockOffset + 8 + bttLen)

	cc := make([]byte, 8)
	if err := br.read(cc); err != nil {
		return err
	}
	entryCount := binary.LittleEndian.Uint32(cc)
	// Each entry occupies at least minBTTEntrySize bytes on disk, so a count
	// the table's own payload cannot hold is structurally impossible.
	maxEntries := uint64(0)
	if bttLen >= 8 {
		maxEntries = (bttLen - 8) / minBTTEntrySize
	}
	if uint64(entryCount) > maxEntries {
		return fmt.Errorf("binpazer: block type table claims %d entries but its %d-byte payload holds at most %d: %w",
			entryCount, bttLen, maxEntries, ErrCapacityExceeded)
	}
	br.Types = make([]TypeEntry, 0, entryCount)
	br.typeIdx = make(map[uint16]int, entryCount)
	for i := uint32(0); i < entryCount; i++ {
		eh := make([]byte, 20)
		if err := br.read(eh); err != nil {
			return err
		}
		var e TypeEntry
		e.TypeID = binary.LittleEndian.Uint16(eh)
		copy(e.GUID[:], eh[4:20])
		lp2 := make([]byte, 4)
		if err := br.read(lp2); err != nil {
			return err
		}
		nlen := uint64(binary.LittleEndian.Uint32(lp2))
		if nlen > 0 {
			if br.off >= bttEnd || nlen > bttEnd-br.off {
				return fmt.Errorf("binpazer: block type table entry %d name (%d bytes) overruns the table: %w",
					i, nlen, ErrCapacityExceeded)
			}
			nb := make([]byte, nlen)
			if err := br.read(nb); err != nil {
				return err
			}
			e.Name = string(nb)
		}
		if err := br.skip(1); err != nil { // trailing NUL
			return err
		}
		raw := 25 + nlen
		if err := br.skip(align8(raw) - raw); err != nil { // entry pad
			return err
		}
		if _, dup := br.typeIdx[e.TypeID]; !dup { // first entry with an id wins
			br.typeIdx[e.TypeID] = len(br.Types)
		}
		br.Types = append(br.Types, e)
	}
	if br.off > bttEnd {
		return errors.New("binpazer: block type table overrun")
	}
	return br.skip(bttEnd - br.off)
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
	br.off += uint64(n)
	if err != nil {
		return nil, br.wrapEOF(err)
	}

	b := &Block{}
	b.TypeID = binary.LittleEndian.Uint16(bh)
	b.Flags = binary.LittleEndian.Uint16(bh[2:])
	b.PayloadLen = decodePayloadLen(uint64(binary.LittleEndian.Uint32(bh[4:])), b.Flags)
	if err := br.claim(payloadClaim(b.PayloadLen, b.Flags), "block payload"); err != nil {
		return nil, err
	}
	b.payloadOff = br.off
	br.resolveType(b)
	return b, nil
}

// resolveType fills a block's GUID and Known from the type table. Duplicate
// ids resolve to the table's first entry, matching the on-disk scan order.
func (br *Reader) resolveType(b *Block) {
	if i, ok := br.typeIdx[b.TypeID]; ok {
		b.GUID = br.Types[i].GUID
		b.Known = true
	}
}

// ReadPayload reads the current block's payload (verifying the CRC if present)
// and advances to the next block.
func (br *Reader) ReadPayload(b *Block) ([]byte, error) {
	if err := br.claim(payloadClaim(b.PayloadLen, b.Flags), "block payload"); err != nil {
		return nil, err
	}
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
			return nil, fmt.Errorf("binpazer: block at offset %d: %w", b.Offset(), ErrCRCMismatch)
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

// InputSize returns the total input size learned by seeking, or -1 when the
// input is not seekable.
func (br *Reader) InputSize() int64 { return br.inputSize }

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
		br.off += uint64(n)
		if err != nil {
			return nil, br.wrapEOF(err)
		}
		tid := binary.LittleEndian.Uint16(bh)
		flags := binary.LittleEndian.Uint16(bh[2:])
		plen := decodePayloadLen(uint64(binary.LittleEndian.Uint32(bh[4:])), flags)
		if err := br.claim(payloadClaim(plen, flags), "block payload"); err != nil {
			return nil, err
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
	if br.inputSize >= 0 && offset > uint64(br.inputSize) {
		return nil, fmt.Errorf("binpazer: block offset %d is beyond the %d-byte input: %w", offset, br.inputSize, ErrTruncated)
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
	b.PayloadLen = decodePayloadLen(uint64(binary.LittleEndian.Uint32(bh[4:])), b.Flags)
	if err := br.claim(payloadClaim(b.PayloadLen, b.Flags), "block payload"); err != nil {
		return nil, err
	}
	b.payloadOff = br.off
	br.resolveType(b)
	return b, nil
}
