// codecbuf.go holds LookupCodec.

package binpazer

// Buffer-to-buffer codec entry points -- the base layer every other
// compression path is built on, here and in the C implementation
// (binpazer_compress / binpazer_decompress).

import (
	"bytes"
	"fmt"
	"io"
)

// LookupCodec returns the codec registered for a registry id and whether one
// is available -- the question.
func LookupCodec(codecID uint16) (Codec, bool) {
	c, err := lookupCodec(Compression{CodecID: codecID})
	return c, err == nil
}

// LookupCodecGUID is LookupCodec for a codec addressed by GUID.
func LookupCodecGUID(g GUID) (Codec, bool) {
	c, err := lookupCodec(Compression{CodecID: CodecByGUID, CodecGUID: g})
	return c, err == nil
}

func CompressBound(codecID uint16, srcLen int) int {
	codec, err := lookupCodec(Compression{CodecID: codecID})
	if err != nil {
		return 0
	}
	return codec.CompressBound(srcLen)
}

// CompressBoundGUID is CompressBound for a codec addressed by GUID.
func CompressBoundGUID(g GUID, srcLen int) int {
	codec, err := lookupCodec(Compression{CodecID: CodecByGUID, CodecGUID: g})
	if err != nil {
		return 0
	}
	return codec.CompressBound(srcLen)
}

// CompressInto encodes src into dst and returns the encoded length. dst too
// small for the result is ErrShortBuffer -- size it with CompressBound.
func CompressInto(codecID uint16, dst, src []byte) (int, error) {
	return compressInto(Compression{CodecID: codecID}, dst, src)
}

// CompressIntoGUID is CompressInto for a codec addressed by GUID.
func CompressIntoGUID(g GUID, dst, src []byte) (int, error) {
	return compressInto(Compression{CodecID: CodecByGUID, CodecGUID: g}, dst, src)
}

func compressInto(c Compression, dst, src []byte) (int, error) {
	codec, err := lookupCodec(c)
	if err != nil {
		return 0, err
	}
	bw := &boundedWriter{dst: dst}
	w, err := codec.NewWriter(bw)
	if err != nil {
		return 0, err
	}
	if _, err := w.Write(src); err != nil {
		w.Close()
		return 0, err
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	return bw.n, nil
}

// DecompressInto decodes an envelope's codec bytes into dst and returns the
// decoded length. c.UncompressedLen is authoritative: a codec that yields any
// other length is corruption. This is also what keeps a decompression bomb
// bounded by a buffer the caller chose.
func DecompressInto(c Compression, dst, stored []byte) (int, error) {
	codec, err := lookupCodec(c)
	if err != nil {
		return 0, err
	}
	if uint64(len(dst)) < c.UncompressedLen {
		return 0, fmt.Errorf("binpazer: destination holds %d bytes, envelope declares %d: %w",
			len(dst), c.UncompressedLen, ErrShortBuffer)
	}
	r, err := codec.NewReader(bytes.NewReader(stored))
	if err != nil {
		return 0, err
	}
	defer r.Close()

	want := int(c.UncompressedLen)
	n, err := io.ReadFull(r, dst[:want])
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return 0, fmt.Errorf("binpazer: decoded payload is %d bytes, envelope declares %d: %w",
			n, want, ErrBadEnvelope)
	}
	if err != nil {
		return 0, err
	}
	// One more read: a codec with bytes left over decodes to more than the envelope declares.
	var probe [1]byte
	if extra, _ := r.Read(probe[:]); extra > 0 {
		return 0, fmt.Errorf("binpazer: decoded payload exceeds the envelope's %d bytes: %w",
			want, ErrBadEnvelope)
	}
	return n, nil
}

// Compress is CompressInto with the destination allocated from the codec's
// bound.
func Compress(codecID uint16, src []byte) ([]byte, error) {
	return compressAlloc(Compression{CodecID: codecID}, src)
}

// CompressGUID is Compress for a codec addressed by GUID.
func CompressGUID(g GUID, src []byte) ([]byte, error) {
	return compressAlloc(Compression{CodecID: CodecByGUID, CodecGUID: g}, src)
}

func compressAlloc(c Compression, src []byte) ([]byte, error) {
	codec, err := lookupCodec(c)
	if err != nil {
		return nil, err
	}
	dst := make([]byte, codec.CompressBound(len(src)))
	n, err := compressInto(c, dst, src)
	if err != nil {
		return nil, err
	}
	return dst[:n], nil
}

// Decompress is DecompressInto with the destination allocated from the
// envelope's declared length.
func Decompress(c Compression, stored []byte) ([]byte, error) {
	dst := make([]byte, c.UncompressedLen)
	n, err := DecompressInto(c, dst, stored)
	if err != nil {
		return nil, err
	}
	return dst[:n], nil
}

// boundedWriter writes into a caller's buffer and refuses to grow it.
type boundedWriter struct {
	dst []byte
	n   int
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if b.n+len(p) > len(b.dst) {
		return 0, fmt.Errorf("binpazer: encoded output exceeds the %d-byte destination: %w",
			len(b.dst), ErrShortBuffer)
	}
	copy(b.dst[b.n:], p)
	b.n += len(p)
	return len(p), nil
}
