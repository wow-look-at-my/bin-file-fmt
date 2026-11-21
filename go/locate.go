// locate.go holds Find and At, the seek-based ways to reach a block.

package binpazer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Find returns the file offsets of all blocks with the given type id. It uses
// the Block Index when present (seeking straight to it) and otherwise walks the
// file. It requires a seekable reader and preserves the reader's position.
func (br *Reader) Find(typeID uint16) ([]uint64, error) {
	if br.seeker == nil {
		return nil, errors.New("binpazer: Find requires a seekable reader")
	}
	saved := br.off
	offsets, ferr := br.find(typeID)
	_ = br.seekTo(saved) // best-effort restore; a find error takes precedence
	if ferr != nil {
		return nil, ferr
	}
	return offsets, nil
}

func (br *Reader) find(typeID uint16) ([]uint64, error) {
	var offsets []uint64
	if br.hasIndex {
		if err := br.seekTo(br.indexOffset); err != nil {
			return nil, err
		}
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
	if err := br.seekTo(br.FirstBlockOffset); err != nil {
		return nil, err
	}
	if br.Appendable() {
		return br.findCommitted(typeID)
	}
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
		if err := br.seekTo(next); err != nil {
			return nil, err
		}
	}
	return offsets, nil
}

// findCommitted walks the blocks of an appendable file and stops where the committed blocks stop.
func (br *Reader) findCommitted(typeID uint16) ([]uint64, error) {
	ended := br.ended
	defer func() { br.ended = ended }() // the walk must not change what the caller's position has seen
	br.ended = false
	var offsets []uint64
	// The Block Type Table has no seal, so the committed walk starts after it.
	if typeID == TypeBlockTypeTable {
		offsets = append(offsets, br.FirstBlockOffset)
	}
	if err := br.seekTo(br.tableEnd); err != nil {
		return nil, err
	}
	for {
		b, err := br.NextCommitted()
		if err == io.EOF || errors.Is(err, ErrPending) {
			return offsets, nil
		}
		if err != nil {
			return nil, err
		}
		if b.TypeID == typeID {
			offsets = append(offsets, b.Offset())
		}
		if err := br.SkipPayload(b); err != nil {
			return nil, err
		}
	}
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
	if err := br.seekTo(offset); err != nil {
		return nil, err
	}
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
