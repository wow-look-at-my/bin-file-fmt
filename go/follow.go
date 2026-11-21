// follow.go holds NextCommitted, the way to read an appendable file while a writer appends to it.

package binpazer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// NextCommitted returns the next committed block of an appendable file.
// A block is committed when all its bytes are in the file and its CRC seal verifies.
// ErrPending means no such block is there yet: call again later, the reader has not moved.
// ErrTorn is the ErrPending case where the bytes are all present but the seal is bad.
// io.EOF means the writer ended the file. The reader must be seekable.
//
// The returned block is positioned for ReadPayload, like a block from Next.
func (br *Reader) NextCommitted() (*Block, error) {
	if br.seeker == nil {
		return nil, errors.New("binpazer: NextCommitted requires a seekable reader")
	}
	if !br.Appendable() {
		return nil, ErrNotAppendable
	}
	if br.ended || (br.blocksEnd != FileLengthUnknown && br.off >= br.blocksEnd) {
		return nil, io.EOF
	}
	start := br.off

	// The file grows between calls, so its size is learned again each time.
	size, err := br.seeker.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("binpazer: determining input size: %w", err)
	}
	br.inputSize = size
	if err := br.seekTo(start); err != nil {
		return nil, err
	}
	b, err := br.verifyCommitted(uint64(size))
	if err != nil {
		if serr := br.seekTo(start); serr != nil {
			return nil, serr
		}
		return nil, err
	}
	if err := br.seekTo(b.payloadOff); err != nil {
		return nil, err
	}
	br.resolveType(b)
	br.noteEnd(b)
	return b, nil
}

// verifyCommitted reads the block at the current offset and checks its seal against a file of size bytes.
func (br *Reader) verifyCommitted(size uint64) (*Block, error) {
	start := br.off
	if size < start+8 {
		return nil, fmt.Errorf("binpazer: offset %d: block header not written: %w", start, ErrPending)
	}
	var bh [8]byte
	if err := br.read(bh[:]); err != nil {
		return nil, err
	}
	b := &Block{payloadOff: br.off}
	b.TypeID = binary.LittleEndian.Uint16(bh[0:])
	b.Flags = binary.LittleEndian.Uint16(bh[2:])
	b.PayloadLen = decodePayloadLen(uint64(binary.LittleEndian.Uint32(bh[4:])), b.Flags)
	if b.TypeID == TypeInvalid {
		// Zeroed space is where a block will be, not a block.
		return nil, fmt.Errorf("binpazer: offset %d: no block written: %w", start, ErrPending)
	}
	if b.Flags&FlagHasCRC == 0 {
		return nil, fmt.Errorf("binpazer: block at offset %d has no CRC seal: %w", start, ErrTorn)
	}
	if b.nextOffset() > size {
		return nil, fmt.Errorf("binpazer: block at offset %d: %d of %d bytes written: %w",
			start, size-start, b.nextOffset()-start, ErrPending)
	}

	crc := crc32.Update(0, crcTable, bh[:])
	buf := make([]byte, min64(b.PayloadLen, ioBufSize))
	for rem := b.PayloadLen; rem > 0; {
		c := min64(rem, uint64(len(buf)))
		if err := br.read(buf[:c]); err != nil {
			return nil, err
		}
		crc = crc32.Update(crc, crcTable, buf[:c])
		rem -= c
	}
	var cb [4]byte
	if err := br.read(cb[:]); err != nil {
		return nil, err
	}
	if crc != binary.LittleEndian.Uint32(cb[:]) {
		return nil, fmt.Errorf("binpazer: block at offset %d: CRC seal does not verify: %w", start, ErrTorn)
	}
	return b, nil
}
