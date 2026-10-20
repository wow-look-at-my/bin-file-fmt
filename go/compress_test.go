package binpazer

import (
	"bytes"
	"errors"
	"github.com/stretchr/testify/require"
	"io"
	"os"
	"strings"
	"testing"
)

// writeTo builds a file in memory with the given block-writing function.
func writeTo(t *testing.T, defs []TypeDef, blocks func(*Writer)) []byte {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "bp-*.binpazer")
	require.Nil(t, err)

	defer f.Close()
	w, err := NewWriter(f, GUID{1}, "test", defs)
	require.Nil(t, err)

	blocks(w)
	require.NoError(t, w.WriteIndex())

	require.NoError(t, w.End())

	b, err := os.ReadFile(f.Name())
	require.Nil(t, err)

	return b
}

func readerOver(t *testing.T, b []byte) *Reader {
	t.Helper()
	r, err := NewReader(bytes.NewReader(b))
	require.Nil(t, err)

	return r
}

var compressDefs = []TypeDef{{TypeID: 1, GUID: GUID{9}, Name: "Data"}}

// TestCompressedRoundTrip covers every codec the library ships, plus the
// uncompressed path through the same reader entry points.
func TestCompressedRoundTrip(t *testing.T) {
	payload := []byte(strings.Repeat("binpazer compresses well. ", 400))

	for _, tc := range []struct {
		name  string
		codec uint16
	}{
		{"stored", CodecStored},
		{"deflate", CodecDeflate},
		{"zstd", CodecZstd},
		{"lz4", CodecLZ4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := writeTo(t, compressDefs, func(w *Writer) {
				require.NoError(t, w.CompressedBlock(1, FlagHasCRC, tc.codec, payload))

			})

			r := readerOver(t, file)
			require.Equal(t, uint16(VersionMinorCompression), r.VersionMinor)

			b, err := r.Next()
			require.Nil(t, err)

			require.NotEqual(t, uint16(0), b.Flags&FlagCompressed)

			got, err := r.ReadDecompressedPayload(b)
			require.Nil(t, err)

			require.True(t, bytes.Equal(got, payload))

			// Every real codec must shrink this input; `stored` is identity
			// and only has to not grow it.
			require.False(t, tc.codec != CodecStored && len(file) >= len(payload),
				"%s produced a %d-byte file for a %d-byte payload", tc.name, len(file), len(payload))

		})
	}
}

// TestCompressedStreamingRead pins that a compressed payload can be streamed
// without materializing it, and that the block CRC (which covers the STORED
// bytes) still verifies through the uncompressed path.
func TestCompressedStreamingRead(t *testing.T) {
	payload := []byte(strings.Repeat("stream me ", 5000))
	file := writeTo(t, compressDefs, func(w *Writer) {
		require.NoError(t, w.CompressedBlock(1, FlagHasCRC, CodecDeflate, payload))

	})

	r := readerOver(t, file)
	b, err := r.Next()
	require.Nil(t, err)

	dr, err := r.DecompressedPayloadReader(b)
	require.Nil(t, err)

	got, err := io.ReadAll(dr)
	require.Nil(t, err)

	require.True(t, bytes.Equal(got, payload))

	require.NoError(t, r.SkipPayload(b))

}

// TestCompressedRandomAccess is the property the format gains over a .tar.gz:
// one member is decoded without touching the bytes of the others.
func TestCompressedRandomAccess(t *testing.T) {
	want := []byte("the last block's payload")
	file := writeTo(t, compressDefs, func(w *Writer) {
		for i := 0; i < 20; i++ {
			require.NoError(t, w.CompressedBlock(1, 0, CodecDeflate, []byte(strings.Repeat("filler ", 1000))))

		}
		require.NoError(t, w.CompressedBlock(1, 0, CodecDeflate, want))

	})

	r := readerOver(t, file)
	offsets, err := r.Find(1)
	require.Nil(t, err)

	require.Equal(t, 21, len(offsets))

	b, err := r.At(offsets[len(offsets)-1])
	require.Nil(t, err)

	got, err := r.ReadDecompressedPayload(b)
	require.Nil(t, err)

	require.True(t, bytes.Equal(got, want))

}

// TestUncompressedFileStaysV10 is the compatibility guarantee: adding
// compression to the library must not change a byte of a file that uses none.
func TestUncompressedFileStaysV10(t *testing.T) {
	file := writeTo(t, compressDefs, func(w *Writer) {
		require.NoError(t, w.Block(1, FlagHasCRC, []byte("plain payload")))

	})
	r := readerOver(t, file)
	require.Equal(t, uint16(0), r.VersionMinor)

	b, err := r.Next()
	require.Nil(t, err)

	require.Equal(t, uint16(0), b.Flags&FlagCompressed)

	// The decompressing entry points pass an uncompressed block straight through.
	got, err := r.ReadDecompressedPayload(b)
	require.Nil(t, err)

	require.Equal(t, "plain payload", string(got))

}

func TestCanonicalSampleUnchanged(t *testing.T) {
	f, err := os.Open("testdata/sample.binpazer")
	require.Nil(t, err)

	defer f.Close()
	r, err := NewReader(f)
	require.Nil(t, err)

	require.False(t, r.VersionMajor != 1 || r.VersionMinor != 0)

	for {
		b, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.Nil(t, err)

		require.Equal(t, uint16(0), b.Flags&FlagCompressed)

		require.NoError(t, r.SkipPayload(b))

	}
}

// fixtureCodecGUID is the private codec GUID the compressed fixture uses for the CodecByGUID escape.
var fixtureCodecGUID = GUID{0xc0, 0xde, 0xc0, 0xde, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 7}

// sample-compressed.binpazer is written by the C reference (c/gen.c). These
// bytes are the cross-implementation contract for the envelope: C, C++ and Go
// must all produce them exactly. It uses codec `stored`, the codec every
// implementation can produce with no dependency.
func writeCompressedSample(w io.Writer) error {
	bw, err := NewWriter(w, GUID{0xaa, 0xbb, 0xcc, 0xdd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
		"demo", []TypeDef{{TypeID: 2, GUID: GUID{0xde, 0xad, 0xbe, 0xef}, Name: "Note"}})
	if err != nil {
		return err
	}
	if err := bw.Block(2, 0, []byte("plain block")); err != nil {
		return err
	}
	if err := bw.CompressedBlock(2, FlagHasCRC, CodecStored, []byte("compressed, sort of")); err != nil {
		return err
	}
	// The fixture's GUID-identified codec is `stored` under a private GUID, so
	// writing (like reading) it requires registering that GUID first.
	if err := RegisterCodecGUID(fixtureCodecGUID, storedCodec{}); err != nil {
		return err
	}
	if err := bw.CompressedBlockGUID(2, 0, fixtureCodecGUID, []byte("guid-codec payload")); err != nil {
		return err
	}
	return bw.End()
}

// TestCrossImplWriteCompressed: Go's envelope bytes must equal the C-written
// fixture's, byte for byte -- including the back-patched version_minor.
func TestCrossImplWriteCompressed(t *testing.T) {
	fixture, err := os.ReadFile("testdata/sample-compressed.binpazer")
	if err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	var m memFile
	require.NoError(t, writeCompressedSample(&m))
	require.True(t, bytes.Equal(m.buf, fixture),
		"Go wrote %d bytes, the C fixture is %d", len(m.buf), len(fixture))
}

// TestCrossImplReadCompressed: Go must read what C wrote, decode both codecs,
// and see the version the C writer back-patched.
func TestCrossImplReadCompressed(t *testing.T) {
	f, err := os.Open("testdata/sample-compressed.binpazer")
	if err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	defer f.Close()
	r, err := NewReader(f)
	require.NoError(t, err)
	require.Equal(t, uint16(VersionMinorCompression), r.VersionMinor)

	// A GUID-identified codec is only readable once registered.
	require.NoError(t, RegisterCodecGUID(fixtureCodecGUID, storedCodec{}))

	for i, want := range []string{"plain block", "compressed, sort of", "guid-codec payload"} {
		b, err := r.Next()
		require.NoError(t, err)
		got, err := r.ReadDecompressedPayload(b)
		require.NoError(t, err)
		require.Equal(t, want, string(got), "block %d", i)
	}
}

// TestUnavailableCodecIsSkippable pins the forward-compatibility contract: a
// codec this build lacks makes only the PAYLOAD unreadable -- the block still
// walks, skips, and reports its codec.
func TestUnavailableCodecIsSkippable(t *testing.T) {
	const futureCodec = 4242
	require.NoError(t, RegisterCodec(futureCodec, storedCodec{}))

	file := writeTo(t, compressDefs, func(w *Writer) {
		require.NoError(t, w.CompressedBlock(1, 0, futureCodec, []byte("from the future")))

		require.NoError(t, w.Block(1, 0, []byte("readable")))

	})
	// Forget the codec, as a build without it would have.
	codecMu.Lock()
	delete(codecs, futureCodec)
	codecMu.Unlock()

	r := readerOver(t, file)
	b, err := r.Next()
	require.Nil(t, err)

	_, err = r.ReadDecompressedPayload(b)
	require.True(t, errors.Is(err, ErrCodecUnavailable))

	// ... and the file is still walkable past it.
	require.NoError(t, r.SkipPayload(b))

	b2, err := r.Next()
	require.Nil(t, err)

	got, err := r.ReadPayload(b2)
	require.Nil(t, err)

	require.Equal(t, "readable", string(got))

}

// TestBadEnvelopeRejected covers the envelope's own validation: a corrupt
// codec id, a set reserved field, and a truncated header are all corruption,
// not silently-empty payloads.
func TestBadEnvelopeRejected(t *testing.T) {
	base := writeTo(t, compressDefs, func(w *Writer) {
		require.NoError(t, w.CompressedBlock(1, 0, CodecDeflate, []byte("payload")))

	})
	// Locate the compressed block's payload: the reader reports its offset.
	r := readerOver(t, base)
	b, err := r.Next()
	require.Nil(t, err)

	payloadOff := b.Offset() + 8

	for _, tc := range []struct {
		name  string
		patch func([]byte)
	}{
		{"codec id 0", func(f []byte) { f[payloadOff] = 0; f[payloadOff+1] = 0 }},
		{"reserved codec_flags set", func(f []byte) { f[payloadOff+2] = 1 }},
		{"reserved u32 set", func(f []byte) { f[payloadOff+4] = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := append([]byte(nil), base...)
			tc.patch(f)
			r := readerOver(t, f)
			blk, err := r.Next()
			require.Nil(t, err)

			_, err = r.ReadDecompressedPayload(blk)
			require.True(t, errors.Is(err, ErrBadEnvelope))

		})
	}
}

// TestDecodeLengthMismatchRejected pins that uncompressed_length is
// authoritative -- a decode that produces a different size is corruption, not
// a short read the caller has to notice.
func TestDecodeLengthMismatchRejected(t *testing.T) {
	base := writeTo(t, compressDefs, func(w *Writer) {
		require.NoError(t, w.CompressedBlock(1, 0, CodecStored, []byte("0123456789")))

	})
	r := readerOver(t, base)
	b, err := r.Next()
	require.Nil(t, err)

	payloadOff := b.Offset() + 8

	// Claim one more decoded byte than the codec can produce.
	f := append([]byte(nil), base...)
	f[payloadOff+8] = 11
	r2 := readerOver(t, f)
	blk, err := r2.Next()
	require.Nil(t, err)

	_, err = r2.ReadDecompressedPayload(blk)
	require.True(t, errors.Is(err, ErrBadEnvelope))

}

// TestGUIDCodecEscape covers the registry escape for codecs outside SPEC.md's
// numeric registry.
func TestGUIDCodecEscape(t *testing.T) {
	g := GUID{0xfe, 0xed, 0xfa, 0xce}
	require.NoError(t, RegisterCodecGUID(g, storedCodec{}))

	file := writeTo(t, compressDefs, func(w *Writer) {
		require.NoError(t, w.CompressedBlockGUID(1, FlagHasCRC, g, []byte("guid codec payload")))

	})
	r := readerOver(t, file)
	b, err := r.Next()
	require.Nil(t, err)

	got, err := r.ReadDecompressedPayload(b)
	require.Nil(t, err)

	require.Equal(t, "guid codec payload", string(got))

}

func TestCompressedNonSeekableNeedsDeclaredVersion(t *testing.T) {
	var buf bytes.Buffer // an io.Writer with no Seek
	w, err := NewWriter(&buf, GUID{1}, "test", compressDefs)
	require.Nil(t, err)

	err = w.CompressedBlock(1, 0, CodecStored, []byte("x"))
	require.NotNil(t, err)

	var buf2 bytes.Buffer
	w2, err := NewWriterVersion(&buf2, GUID{1}, "test", compressDefs, VersionMinorCompression)
	require.Nil(t, err)

	require.NoError(t, w2.CompressedBlock(1, 0, CodecStored, []byte("x")))

	require.NoError(t, w2.End())

	r := readerOver(t, buf2.Bytes())
	require.Equal(t, uint16(VersionMinorCompression), r.VersionMinor)

}
