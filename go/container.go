// container.go holds ErrBlockNotFound.

package binpazer

// Helpers for the shape most binpazer files take: content blocks written
// once.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrBlockNotFound reports that a file holds no block of the requested type.
var ErrBlockNotFound = errors.New("binpazer: no block of the requested type")

// ErrWrongBlockType reports that a block is not the type.
var ErrWrongBlockType = errors.New("binpazer: block has the wrong type")

// NewReaderAt opens a reader over a random-access blob: an mmap, an *os.File,
// a []byte through bytes.NewReader.
func NewReaderAt(ra io.ReaderAt, size int64) (*Reader, error) {
	return NewReader(io.NewSectionReader(ra, 0, size))
}

// FindFirst seeks to the first block with this type id and returns its
// header, positioned for a payload read.
func (br *Reader) FindFirst(typeID uint16) (*Block, error) {
	return br.findOne(typeID, true)
}

// FindLast seeks to the LAST block with this type id -- the one a rewrite
// appends, so the newest directory or manifest wins over any.
func (br *Reader) FindLast(typeID uint16) (*Block, error) {
	return br.findOne(typeID, false)
}

func (br *Reader) findOne(typeID uint16, first bool) (*Block, error) {
	offsets, err := br.Find(typeID)
	if err != nil {
		return nil, err
	}
	if len(offsets) == 0 {
		return nil, fmt.Errorf("binpazer: type %d: %w", typeID, ErrBlockNotFound)
	}
	at := offsets[len(offsets)-1]
	if first {
		at = offsets[0]
	}
	return br.At(at)
}

// BlockAt seeks to a block by offset and checks its type -- the read half of a
// directory lookup. A mismatch is ErrWrongBlockType, which says the directory
// and the blocks disagree rather than blaming the caller.
func (br *Reader) BlockAt(offset uint64, wantTypeID uint16) (*Block, error) {
	b, err := br.At(offset)
	if err != nil {
		return nil, err
	}
	if b.TypeID != wantTypeID {
		return nil, fmt.Errorf("binpazer: offset %d holds type %d, wanted %d: %w",
			offset, b.TypeID, wantTypeID, ErrWrongBlockType)
	}
	return b, nil
}

// DecodeJSON reads a block's payload -- decompressing it when the block is
// compressed -- and unmarshals it into v, advancing past the block.
func (br *Reader) DecodeJSON(b *Block, v any) error {
	payload, err := br.ReadDecompressedPayload(b)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, v); err != nil {
		return fmt.Errorf("binpazer: decode type %d block: %w", b.TypeID, err)
	}
	return nil
}

// DecodeJSONLast finds the last block of a type and decodes it -- how a
// container reads the directory or manifest describing the rest of itself.
func (br *Reader) DecodeJSONLast(typeID uint16, v any) error {
	b, err := br.FindLast(typeID)
	if err != nil {
		return err
	}
	return br.DecodeJSON(b, v)
}

// OpenAt returns a reader over one block's payload, decompressing when needed,
// after checking the block's type. The whole read half of a directory lookup in
// one call. The payload is streamed, not buffered.
func (br *Reader) OpenAt(offset uint64, wantTypeID uint16) (io.Reader, *Block, error) {
	b, err := br.BlockAt(offset, wantTypeID)
	if err != nil {
		return nil, nil, err
	}
	r, err := br.DecompressedPayloadReader(b)
	if err != nil {
		return nil, b, err
	}
	return r, b, nil
}

// Put writes a block. Returns the offset it landed at -- what a directory
// records so a later read is a seek instead of a walk. Block is the same write
// for a caller with nothing to record.
func (bw *Writer) Put(typeID, flags uint16, payload []byte) (uint64, error) {
	off := bw.Offset()
	if err := bw.Block(typeID, flags, payload); err != nil {
		return 0, err
	}
	return off, nil
}

// PutCompressed is Put for a compressed block.
func (bw *Writer) PutCompressed(typeID, flags, codecID uint16, payload []byte) (uint64, error) {
	off := bw.Offset()
	if err := bw.CompressedBlock(typeID, flags, codecID, payload); err != nil {
		return 0, err
	}
	return off, nil
}

// PutJSON marshals v and writes it as one compressed block, returning its
// offset. Pass CodecStored to skip compression while keeping the envelope.
func (bw *Writer) PutJSON(typeID, flags, codecID uint16, v any) (uint64, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return 0, fmt.Errorf("binpazer: encode type %d block: %w", typeID, err)
	}
	return bw.PutCompressed(typeID, flags, codecID, payload)
}

// Finish writes the Block Index and ends the file -- the pair every seekable
// container wants, where forgetting the index costs random access silently.
func (bw *Writer) Finish() error {
	if err := bw.WriteIndex(); err != nil {
		return err
	}
	return bw.End()
}
