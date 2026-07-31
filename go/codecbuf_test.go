package binpazer

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var bufCodecs = []struct {
	name string
	id   uint16
}{
	{"stored", CodecStored},
	{"deflate", CodecDeflate},
	{"zstd", CodecZstd},
	{"lz4", CodecLZ4},
}

// incompressible returns pseudo-random bytes, which is the case a bound has to
// survive: every codec expands input it cannot model.
func incompressible(n int) []byte {
	rng := rand.New(rand.NewSource(int64(n) + 1))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Intn(256))
	}
	return b
}

func compressibleBytes(n int) []byte {
	const line = "binpazer compression parity across implementations. "
	b := make([]byte, n)
	for i := range b {
		b[i] = line[i%len(line)]
	}
	return b
}

// CompressBound must never under-report, including for input no codec can
// shrink -- that is the whole contract letting CompressInto target a buffer
// the caller sized.
func TestCompressBoundCoversIncompressibleInput(t *testing.T) {
	sizes := []int{0, 1, 15, 1024, 64 << 10, 300 << 10}
	for _, c := range bufCodecs {
		for _, n := range sizes {
			src := incompressible(n)
			dst := make([]byte, CompressBound(c.id, n))
			got, err := CompressInto(c.id, dst, src)
			require.NoError(t, err, "%s at %d bytes", c.name, n)
			assert.LessOrEqual(t, got, len(dst), "%s at %d bytes", c.name, n)

			back := make([]byte, n)
			env := Compression{CodecID: c.id, UncompressedLen: uint64(n), StoredLen: uint64(got)}
			decoded, err := DecompressInto(env, back, dst[:got])
			require.NoError(t, err, "%s at %d bytes", c.name, n)
			assert.Equal(t, n, decoded)
			assert.Equal(t, src, back)
		}
	}
}

// The allocating wrappers must agree with the buffer layer they are built on.
func TestCompressDecompressAllocRoundTrip(t *testing.T) {
	src := compressibleBytes(8192)
	for _, c := range bufCodecs {
		stored, err := Compress(c.id, src)
		require.NoError(t, err, c.name)
		if c.id != CodecStored {
			assert.Less(t, len(stored), len(src), "%s should shrink compressible input", c.name)
		}
		env := Compression{CodecID: c.id, UncompressedLen: uint64(len(src)), StoredLen: uint64(len(stored))}
		back, err := Decompress(env, stored)
		require.NoError(t, err, c.name)
		assert.Equal(t, src, back)
	}
}

func TestCompressIntoShortBuffer(t *testing.T) {
	src := compressibleBytes(4096)
	for _, c := range bufCodecs {
		_, err := CompressInto(c.id, make([]byte, 4), src)
		require.ErrorIs(t, err, ErrShortBuffer, c.name)
	}
}

func TestDecompressIntoShortBuffer(t *testing.T) {
	src := compressibleBytes(1024)
	stored, err := Compress(CodecZstd, src)
	require.NoError(t, err)
	env := Compression{CodecID: CodecZstd, UncompressedLen: uint64(len(src)), StoredLen: uint64(len(stored))}
	_, err = DecompressInto(env, make([]byte, len(src)-1), stored)
	require.ErrorIs(t, err, ErrShortBuffer)
}

// The envelope's length is authoritative in both directions: a codec that
// yields fewer or more bytes than declared is corruption, never a truncated
// success.
func TestDecompressIntoLengthMismatch(t *testing.T) {
	src := compressibleBytes(1024)
	stored, err := Compress(CodecDeflate, src)
	require.NoError(t, err)

	short := Compression{CodecID: CodecDeflate, UncompressedLen: uint64(len(src)) - 8, StoredLen: uint64(len(stored))}
	_, err = DecompressInto(short, make([]byte, len(src)), stored)
	require.ErrorIs(t, err, ErrBadEnvelope)

	long := Compression{CodecID: CodecDeflate, UncompressedLen: uint64(len(src)) + 8, StoredLen: uint64(len(stored))}
	_, err = DecompressInto(long, make([]byte, len(src)+8), stored)
	require.ErrorIs(t, err, ErrBadEnvelope)
}

func TestCompressBoundUnavailableCodec(t *testing.T) {
	assert.Zero(t, CompressBound(4242, 100))
	assert.Zero(t, CompressBoundGUID(GUID{0xFE}, 100))

	_, err := CompressInto(4242, make([]byte, 64), []byte("x"))
	require.ErrorIs(t, err, ErrCodecUnavailable)
}

// A GUID-addressed codec reaches the same buffer layer as a registry one.
func TestCompressIntoGUIDCodec(t *testing.T) {
	g := GUID{0xC0, 0xDE, 0xC0, 0xDE}
	require.NoError(t, RegisterCodecGUID(g, storedCodec{}))
	src := []byte("guid codec bytes")

	dst := make([]byte, CompressBoundGUID(g, len(src)))
	n, err := CompressIntoGUID(g, dst, src)
	require.NoError(t, err)
	assert.Equal(t, len(src), n)

	stored, err := CompressGUID(g, src)
	require.NoError(t, err)
	env := Compression{CodecID: CodecByGUID, CodecGUID: g, UncompressedLen: uint64(len(src)), StoredLen: uint64(len(stored))}
	back, err := Decompress(env, stored)
	require.NoError(t, err)
	assert.Equal(t, src, back)
}
