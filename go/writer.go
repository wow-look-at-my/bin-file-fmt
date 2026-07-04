package binpazer

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
)

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
