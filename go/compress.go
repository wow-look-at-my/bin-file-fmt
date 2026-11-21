// compress.go holds FlagCompressed.

package binpazer

import (
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

// FlagCompressed (block flag a later bit) marks a payload as a Compression Envelope.
const FlagCompressed uint16 = 1 << 2

// VersionMinorCompression is the minor version a file must declare once it contains a compressed block.
const VersionMinorCompression = 1

// Codec ids from SPEC.md's registry. CodecByGUID is the escape for codecs
// outside it: the envelope then carries a 16-byte GUID.
const (
	CodecInvalid uint16 = 0
	CodecStored  uint16 = 1
	CodecDeflate uint16 = 2
	CodecZstd    uint16 = 3
	CodecLZ4     uint16 = 4
	CodecByGUID  uint16 = 65535
)

// Envelope header sizes: the fixed part, and the fixed part plus the GUID the
// CodecByGUID escape appends.
const (
	envelopeFixedSize = 16
	envelopeGUIDSize  = 32
)

// Compression describes a compressed block's envelope.
type Compression struct {
	CodecID uint16
	// CodecGUID identifies the codec when CodecID is CodecByGUID; otherwise zero.
	CodecGUID GUID
	// UncompressedLen is the exact size of the decoded payload.
	UncompressedLen uint64
	// StoredLen is the size of the codec bytes as stored (the block payload minus the envelope header).
	StoredLen uint64
}

// headerSize is the envelope header size implied by the codec id.
func (c Compression) headerSize() uint64 {
	if c.CodecID == CodecByGUID {
		return envelopeGUIDSize
	}
	return envelopeFixedSize
}

// Codec compresses and decompresses one codec's byte stream.
type Codec interface {
	// NewReader decodes r. The returned closer is closed after the payload is consumed; closing it must not close r.
	NewReader(r io.Reader) (io.ReadCloser, error)
	// NewWriter encodes into w. Close flushes the codec's trailer.
	NewWriter(w io.Writer) (io.WriteCloser, error)
	// CompressBound reports a size that encoding srcLen bytes cannot exceed, including the codec's own framing.
	CompressBound(srcLen int) int
}

var (
	codecMu sync.RWMutex
	codecs  = map[uint16]Codec{
		CodecStored:  storedCodec{},
		CodecDeflate: deflateCodec{},
		CodecZstd:    zstdCodec{},
		CodecLZ4:     lz4Codec{},
	}
	guidCodecs = map[GUID]Codec{}
)

// RegisterCodec registers a codec for a registry id. Every id SPEC.md assigns
// is already built in. This is for a codec the registry does not name (a
// later revision's, ahead of an upgrade) or to override a built-in with a
// differently-tuned encoder.
func RegisterCodec(id uint16, c Codec) error {
	if id == CodecInvalid {
		return errors.New("binpazer: codec id 0 is invalid")
	}
	if id == CodecByGUID {
		return errors.New("binpazer: register GUID-identified codecs with RegisterCodecGUID")
	}
	if c == nil {
		return errors.New("binpazer: nil codec")
	}
	codecMu.Lock()
	defer codecMu.Unlock()
	codecs[id] = c
	return nil
}

// RegisterCodecGUID registers a codec addressed by GUID (envelope codec id
// CodecByGUID), for codecs outside SPEC.md's registry.
func RegisterCodecGUID(g GUID, c Codec) error {
	if c == nil {
		return errors.New("binpazer: nil codec")
	}
	codecMu.Lock()
	defer codecMu.Unlock()
	guidCodecs[g] = c
	return nil
}

// lookupCodec resolves an envelope's codec, or reports that it is unavailable.
func lookupCodec(c Compression) (Codec, error) {
	codecMu.RLock()
	defer codecMu.RUnlock()
	if c.CodecID == CodecByGUID {
		if cd, ok := guidCodecs[c.CodecGUID]; ok {
			return cd, nil
		}
		return nil, fmt.Errorf("binpazer: codec %s: %w", c.CodecGUID, ErrCodecUnavailable)
	}
	if cd, ok := codecs[c.CodecID]; ok {
		return cd, nil
	}
	return nil, fmt.Errorf("binpazer: codec id %d: %w", c.CodecID, ErrCodecUnavailable)
}

// storedCodec is the identity codec (SPEC registry one id): every implementation can write a well-formed envelope with it.
type storedCodec struct{}

func (storedCodec) NewReader(r io.Reader) (io.ReadCloser, error) {
	return io.NopCloser(r), nil
}

func (storedCodec) NewWriter(w io.Writer) (io.WriteCloser, error) {
	return nopWriteCloser{w}, nil
}

func (storedCodec) CompressBound(srcLen int) int { return srcLen }

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// deflateCodec is raw DEFLATE (RFC 1951, SPEC registry a later id) -- the real codec the Go standard library provides.
type deflateCodec struct{}

func (deflateCodec) NewReader(r io.Reader) (io.ReadCloser, error) {
	return flate.NewReader(r), nil
}

func (deflateCodec) NewWriter(w io.Writer) (io.WriteCloser, error) {
	return flate.NewWriter(w, flate.DefaultCompression)
}

func (deflateCodec) CompressBound(srcLen int) int {
	return srcLen + srcLen/16 + 5*(srcLen/16384+1) + 64
}

// zstdCodec is zstd (RFC 8878, SPEC registry a later id), the codec worth reaching for by default.
type zstdCodec struct{}

func (zstdCodec) NewReader(r io.Reader) (io.ReadCloser, error) {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return nil, err
	}
	// IOReadCloser's Close releases the decoder's goroutines without closing r, which is what the Codec contract requires.
	return zr.IOReadCloser(), nil
}

func (zstdCodec) NewWriter(w io.Writer) (io.WriteCloser, error) {
	return zstd.NewWriter(w)
}

// CompressBound is ZSTD_COMPRESSBOUND plus room for the frame header and a
// final empty block.
func (zstdCodec) CompressBound(srcLen int) int {
	margin := 0
	if srcLen < 128<<10 {
		margin = ((128 << 10) - srcLen) >> 11
	}
	return srcLen + srcLen/128 + margin + 64
}

// lz4Codec is LZ4 (frame format, SPEC registry a later id): a weaker ratio than zstd.
type lz4Codec struct{}

func (lz4Codec) NewReader(r io.Reader) (io.ReadCloser, error) {
	return io.NopCloser(lz4.NewReader(r)), nil
}

func (lz4Codec) NewWriter(w io.Writer) (io.WriteCloser, error) {
	return lz4.NewWriter(w), nil
}

func (lz4Codec) CompressBound(srcLen int) int {
	blocks := srcLen/(64<<10) + 1
	return srcLen + srcLen/255 + blocks*4 + 64
}

// encodeEnvelopeHeader serializes the fixed envelope header.
func encodeEnvelopeHeader(c Compression) []byte {
	hdr := make([]byte, c.headerSize())
	binary.LittleEndian.PutUint16(hdr[0:], c.CodecID)
	binary.LittleEndian.PutUint64(hdr[8:], c.UncompressedLen)
	if c.CodecID == CodecByGUID {
		copy(hdr[16:32], c.CodecGUID[:])
	}
	return hdr
}

// parseEnvelopeHeader decodes the fixed envelope header from the front of a
// stored payload. storedPayloadLen is the block's payload length, used to
// derive the codec byte count and to reject a truncated envelope.
func parseEnvelopeHeader(hdr []byte, storedPayloadLen uint64) (Compression, error) {
	var c Compression
	c.CodecID = binary.LittleEndian.Uint16(hdr[0:])
	if c.CodecID == CodecInvalid {
		return c, fmt.Errorf("binpazer: compression envelope: codec id 0: %w", ErrBadEnvelope)
	}
	if binary.LittleEndian.Uint16(hdr[2:]) != 0 || binary.LittleEndian.Uint32(hdr[4:]) != 0 {
		return c, fmt.Errorf("binpazer: compression envelope: reserved field set: %w", ErrBadEnvelope)
	}
	c.UncompressedLen = binary.LittleEndian.Uint64(hdr[8:])
	if c.CodecID == CodecByGUID {
		copy(c.CodecGUID[:], hdr[16:32])
	}
	if storedPayloadLen < c.headerSize() {
		return c, fmt.Errorf("binpazer: compression envelope: payload holds %d bytes, header needs %d: %w",
			storedPayloadLen, c.headerSize(), ErrBadEnvelope)
	}
	c.StoredLen = storedPayloadLen - c.headerSize()
	return c, nil
}

// Compression reads the current block's Compression Envelope header and
// returns it, leaving the reader positioned at the codec bytes. It is the
// low-level entry point; DecompressedPayloadReader and ReadDecompressedPayload
// are what a caller normally wants.
func (br *Reader) Compression(b *Block) (Compression, error) {
	if b.Flags&FlagCompressed == 0 {
		return Compression{}, fmt.Errorf("binpazer: block at offset %d is not compressed: %w", b.Offset(), ErrBadEnvelope)
	}
	if b.PayloadLen < envelopeFixedSize {
		return Compression{}, fmt.Errorf("binpazer: block at offset %d: payload holds %d bytes, envelope header needs %d: %w",
			b.Offset(), b.PayloadLen, envelopeFixedSize, ErrBadEnvelope)
	}
	hdr := make([]byte, envelopeGUIDSize)
	if err := br.read(hdr[:envelopeFixedSize]); err != nil {
		return Compression{}, err
	}
	if binary.LittleEndian.Uint16(hdr[0:]) == CodecByGUID {
		if b.PayloadLen < envelopeGUIDSize {
			return Compression{}, fmt.Errorf("binpazer: block at offset %d: payload holds %d bytes, GUID envelope header needs %d: %w",
				b.Offset(), b.PayloadLen, envelopeGUIDSize, ErrBadEnvelope)
		}
		if err := br.read(hdr[envelopeFixedSize:]); err != nil {
			return Compression{}, err
		}
	}
	return parseEnvelopeHeader(hdr, b.PayloadLen)
}

// DecompressedPayloadReader returns a reader over the current block's decoded
// payload, decompressing as it is read.
func (br *Reader) DecompressedPayloadReader(b *Block) (io.Reader, error) {
	_, r, err := br.DecompressedPayload(b)
	return r, err
}

// DecompressedPayload is DecompressedPayloadReader plus the envelope it read,
// for a caller that must report or record the codec (an uncompressed block
// reports a zero Compression).
func (br *Reader) DecompressedPayload(b *Block) (Compression, io.Reader, error) {
	if b.Flags&FlagCompressed == 0 {
		r, err := br.PayloadReader(b)
		return Compression{}, r, err
	}
	if err := br.claim(payloadClaim(b.PayloadLen, b.Flags), "block payload"); err != nil {
		return Compression{}, nil, err
	}
	c, err := br.Compression(b)
	if err != nil {
		return Compression{}, nil, err
	}
	codec, err := lookupCodec(c)
	if err != nil {
		return c, nil, err
	}
	dr := &decompressReader{rem: c.UncompressedLen, want: c.UncompressedLen}
	stored := &payloadReader{br: br, rem: c.StoredLen}
	if b.Flags&FlagHasCRC != 0 {
		// The envelope parsed with every reserved field zero, so re-encoding it gives the bytes on disk.
		hb := b.headerBytes()
		dr.crc = crc32.Update(crc32.Update(0, crcTable, hb[:]), crcTable, encodeEnvelopeHeader(c))
		dr.br, dr.b, dr.stored = br, b, stored
		// Set before the codec starts: a decoder may read stored from its own goroutine.
		stored.crc = &dr.crc
	}
	dec, err := codec.NewReader(stored)
	if err != nil {
		return c, nil, err
	}
	dr.dec = dec
	return c, dr, nil
}

// decompressReader bounds a codec's output at the envelope's declared length
// and reports a short or long decode as corruption.
type decompressReader struct {
	dec  io.ReadCloser
	rem  uint64
	want uint64
	got  uint64

	// Set only for a has_crc block.
	br     *Reader
	b      *Block
	stored *payloadReader
	crc    uint32
}

// verifyStored reads the codec bytes the decoder left behind, then the CRC trailer.
// Call it only after dec is closed, so no decoder goroutine still reads stored.
func (dr *decompressReader) verifyStored() error {
	if dr.stored == nil {
		return nil
	}
	stored := dr.stored
	dr.stored = nil
	if _, err := io.Copy(io.Discard, stored); err != nil {
		return err
	}
	return dr.br.verifyCRC(dr.b, dr.crc)
}

func (dr *decompressReader) Read(p []byte) (int, error) {
	if dr.rem == 0 {
		// One more read to catch a codec that would produce extra bytes.
		var probe [1]byte
		n, err := dr.dec.Read(probe[:])
		dr.dec.Close()
		if n > 0 {
			return 0, fmt.Errorf("binpazer: decoded payload exceeds the envelope's %d bytes: %w", dr.want, ErrBadEnvelope)
		}
		if err != nil && err != io.EOF {
			return 0, err
		}
		// Like WritePayloadTo, a CRC failure arrives after the last decoded byte.
		if err := dr.verifyStored(); err != nil {
			return 0, err
		}
		return 0, io.EOF
	}
	if uint64(len(p)) > dr.rem {
		p = p[:dr.rem]
	}
	n, err := dr.dec.Read(p)
	dr.rem -= uint64(n)
	dr.got += uint64(n)
	if err == io.EOF {
		dr.dec.Close()
		if dr.rem > 0 {
			return n, fmt.Errorf("binpazer: decoded payload is %d bytes, envelope declares %d: %w", dr.got, dr.want, ErrBadEnvelope)
		}
		err = nil // the next Read reports the clean EOF
	}
	return n, err
}

// ReadDecompressedPayload reads the current block's payload, decompressing it
// when the block is compressed, and advances to the next block. The allocation
// is sized by the envelope's UncompressedLen, checked against the reader's
// capacity limit first.
func (br *Reader) ReadDecompressedPayload(b *Block) ([]byte, error) {
	if b.Flags&FlagCompressed == 0 {
		return br.ReadPayload(b)
	}
	r, err := br.DecompressedPayloadReader(b)
	if err != nil {
		return nil, err
	}
	c := r.(*decompressReader)
	if err := br.claimDecoded(c.want); err != nil {
		return nil, err
	}
	// Grown by append rather than preallocated to UncompressedLen: the field is untrusted.
	var out []byte
	buf := make([]byte, min64(c.want, ioBufSize))
	for {
		n, rerr := r.Read(buf)
		out = append(out, buf[:n]...)
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	if err := br.SkipPayload(b); err != nil {
		return nil, err
	}
	return out, nil
}

// claimDecoded is the allocation gate for DECOMPRESSED bytes.
func (br *Reader) claimDecoded(n uint64) error {
	if n > uint64(br.maxAlloc) {
		return fmt.Errorf("binpazer: decompressed payload claims %d bytes, over the %d-byte cap: %w", n, br.maxAlloc, ErrCapacityExceeded)
	}
	return nil
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// CompressedBlock writes one block whose payload is compressed with the given
// codec, wrapped in a Compression Envelope. flags must not already contain
// FlagCompressed; it is added.
//
// On a non-seekable writer, declare the version up front with
// NewWriterVersion.
func (bw *Writer) CompressedBlock(typeID, flags, codecID uint16, payload []byte) error {
	if bw.err != nil {
		return bw.err
	}
	if bw.seeker == nil && bw.versionMinor < VersionMinorCompression {
		bw.err = errors.New("binpazer: a compressed block on a non-seekable writer needs NewWriterVersion(..., VersionMinorCompression)")
		return bw.err
	}
	c := Compression{CodecID: codecID, UncompressedLen: uint64(len(payload))}
	stored, err := compressAlloc(c, payload)
	if err != nil {
		bw.err = err
		return err
	}
	body := append(encodeEnvelopeHeader(c), stored...)
	bw.usedCompression = true
	return bw.Block(typeID, flags|FlagCompressed, body)
}

// CompressedBlockGUID is CompressedBlock for a codec outside SPEC.md's
// registry, identified by GUID (envelope codec id CodecByGUID).
func (bw *Writer) CompressedBlockGUID(typeID, flags uint16, codecGUID GUID, payload []byte) error {
	if bw.err != nil {
		return bw.err
	}
	if bw.seeker == nil && bw.versionMinor < VersionMinorCompression {
		bw.err = errors.New("binpazer: a compressed block on a non-seekable writer needs NewWriterVersion(..., VersionMinorCompression)")
		return bw.err
	}
	c := Compression{CodecID: CodecByGUID, CodecGUID: codecGUID, UncompressedLen: uint64(len(payload))}
	stored, err := compressAlloc(c, payload)
	if err != nil {
		bw.err = err
		return err
	}
	body := append(encodeEnvelopeHeader(c), stored...)
	bw.usedCompression = true
	return bw.Block(typeID, flags|FlagCompressed, body)
}
