// append.go holds the writer side of appendable files.

package binpazer

import (
	"bufio"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
)

// endTypeName is the name an appendable file gives the End type in its Block Type Table.
const endTypeName = "End"

// maxPadExtent caps one Padding block of ResumeAppendWriter, so its length field cannot overflow.
const maxPadExtent = 1 << 20

// NewAppendWriter starts an appendable file: a file that grows block by block
// while readers follow it.
func NewAppendWriter(w io.Writer, writerGUID GUID, name string, defs []TypeDef) (*Writer, error) {
	return newWriter(w, writerGUID, name, defs, VersionMinorAppendable, true)
}

// ResumeAppendWriter continues an appendable file that an earlier writer left open.
// It walks to the end of the committed blocks and seals any torn tail inside Padding blocks.
// It returns the file's block types, so the caller can check its type ids against them.
// An ended file is ErrEnded.
func ResumeAppendWriter(rw io.ReadWriteSeeker) (*Writer, []TypeEntry, error) {
	br, err := NewReader(rw)
	if err != nil {
		return nil, nil, err
	}
	for {
		b, err := br.NextCommitted()
		if errors.Is(err, ErrPending) {
			break
		}
		if err == io.EOF {
			return nil, nil, ErrEnded
		}
		if err != nil {
			return nil, nil, err
		}
		if err := br.SkipPayload(b); err != nil {
			return nil, nil, err
		}
	}
	end := br.Offset()
	if _, err := rw.Seek(int64(end), io.SeekStart); err != nil {
		return nil, nil, err
	}
	bw := &Writer{
		dst: rw, w: bufio.NewWriterSize(rw, ioBufSize), seeker: rw,
		off: end, versionMinor: br.VersionMinor, appendable: true,
	}
	if size := uint64(br.InputSize()); size > end {
		bw.padOver(size - end)
	}
	return bw, br.Types, bw.err
}

// padOver writes sealed Padding blocks over the next n bytes of the destination.
// A torn tail can hold sealed nested blocks, and a later short block must not expose one as committed.
func (bw *Writer) padOver(n uint64) {
	var zero [4096]byte
	for n > 0 && bw.err == nil {
		extent := align8(n)
		if extent < 16 {
			extent = 16
		}
		if extent > maxPadExtent {
			extent = maxPadExtent
		}
		payloadLen := extent - 12 // header and CRC trailer fill the rest; no alignment pad is left

		var bh [8]byte
		binary.LittleEndian.PutUint16(bh[0:], TypePadding)
		binary.LittleEndian.PutUint16(bh[2:], FlagHasCRC)
		binary.LittleEndian.PutUint32(bh[4:], uint32(payloadLen))
		bw.write(bh[:])
		crc := crc32.Update(0, crcTable, bh[:])
		for rem := payloadLen; rem > 0; {
			c := min64(rem, uint64(len(zero)))
			bw.write(zero[:c])
			crc = crc32.Update(crc, crcTable, zero[:c])
			rem -= c
		}
		var cb [4]byte
		binary.LittleEndian.PutUint32(cb[:], crc)
		bw.write(cb[:])

		if extent >= n {
			break
		}
		n -= extent
	}
	bw.flush()
}
