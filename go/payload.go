// payload.go holds headerBytes.

package binpazer

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
)

// headerBytes reconstructs the block's on-disk 8-byte header, which prefixes
// the payload in the CRC-32C computation when FlagHasCRC is set.
func (b *Block) headerBytes() [8]byte {
	var lf uint32
	if b.Flags&FlagLargeMode != 0 {
		lf = uint32(b.PayloadLen / LargeUnit)
	} else {
		lf = uint32(b.PayloadLen)
	}
	var bh [8]byte
	binary.LittleEndian.PutUint16(bh[0:], b.TypeID)
	binary.LittleEndian.PutUint16(bh[2:], b.Flags)
	binary.LittleEndian.PutUint32(bh[4:], lf)
	return bh
}

// verifyCRC reads the block's stored CRC and compares it to the computed one.
func (br *Reader) verifyCRC(b *Block, crc uint32) error {
	var cb [4]byte
	if err := br.read(cb[:]); err != nil {
		return err
	}
	if crc != binary.LittleEndian.Uint32(cb[:]) {
		return fmt.Errorf("binpazer: block at offset %d: %w", b.Offset(), ErrCRCMismatch)
	}
	return nil
}

// It returns the number of payload bytes written to w.
//
// The payload bytes reach w before the trailing CRC can be checked, so on
// ErrCRCMismatch the caller must discard whatever it received.
func (br *Reader) WritePayloadTo(b *Block, w io.Writer) (int64, error) {
	if err := br.claim(payloadClaim(b.PayloadLen, b.Flags), "block payload"); err != nil {
		return 0, err
	}
	hasCRC := b.Flags&FlagHasCRC != 0
	var crc uint32
	if hasCRC {
		hb := b.headerBytes()
		crc = crc32.Update(0, crcTable, hb[:])
	}
	bufSize := uint64(ioBufSize)
	if b.PayloadLen < bufSize {
		bufSize = b.PayloadLen
	}
	buf := make([]byte, bufSize)
	var written int64
	for rem := b.PayloadLen; rem > 0; {
		c := uint64(len(buf))
		if c > rem {
			c = rem
		}
		if err := br.read(buf[:c]); err != nil {
			return written, err
		}
		if hasCRC {
			crc = crc32.Update(crc, crcTable, buf[:c])
		}
		n, err := w.Write(buf[:c])
		written += int64(n)
		if err != nil {
			return written, err
		}
		rem -= c
	}
	if hasCRC {
		if err := br.verifyCRC(b, crc); err != nil {
			return written, err
		}
	}
	if err := br.skip(b.nextOffset() - br.off); err != nil {
		return written, err
	}
	return written, nil
}

// PayloadReader returns a reader that yields exactly the current block's
// payload bytes, without buffering the payload and without CRC verification
// (use WritePayloadTo to verify while streaming).
func (br *Reader) PayloadReader(b *Block) (io.Reader, error) {
	if err := br.claim(payloadClaim(b.PayloadLen, b.Flags), "block payload"); err != nil {
		return nil, err
	}
	return &payloadReader{br: br, rem: b.PayloadLen}, nil
}

// payloadReader is a bounded reader over one block's payload.
type payloadReader struct {
	br  *Reader
	rem uint64
}

func (pr *payloadReader) Read(p []byte) (int, error) {
	if pr.rem == 0 {
		return 0, io.EOF
	}
	if uint64(len(p)) > pr.rem {
		p = p[:pr.rem]
	}
	n, err := pr.br.r.Read(p)
	pr.br.off += uint64(n)
	pr.rem -= uint64(n)
	if err == io.EOF {
		if pr.rem > 0 {
			err = pr.br.wrapEOF(io.ErrUnexpectedEOF) // input cut mid-payload
		} else {
			err = nil // payload complete; the next Read reports a clean EOF
		}
	}
	return n, err
}
