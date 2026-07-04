package binpazer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Byte offsets inside the canonical 280-byte sample (see writeSample):
// the header's file_length, the writer-name length, the BTT entry count,
// the first data block's header/payload/CRC, and the first BTT entry's
// name length.
const (
	sampleOffFileLength = 32
	sampleOffNameLen    = 56
	sampleOffBTTCount   = 80
	sampleOffEntry0Name = 108
	sampleOffBlock1     = 192
	sampleOffPayload1   = 200
	sampleOffCRC1       = 216
)

// sampleBytes returns a fresh copy of the canonical sample, sanity-checking
// the offsets the corruption helpers patch (so a fixture change fails loudly).
func sampleBytes(t testing.TB) []byte {
	t.Helper()
	var m memFile
	require.NoError(t, writeSample(&m))
	require.Equal(t, 280, len(m.buf))
	require.Equal(t, uint32(3), binary.LittleEndian.Uint32(m.buf[sampleOffBTTCount:]))
	require.Equal(t, uint32(len("BlockTypeTable")), binary.LittleEndian.Uint32(m.buf[sampleOffEntry0Name:]))
	require.Equal(t, uint16(1), binary.LittleEndian.Uint16(m.buf[sampleOffBlock1:]))
	require.Equal(t, uint32(16), binary.LittleEndian.Uint32(m.buf[sampleOffBlock1+4:]))
	return append([]byte(nil), m.buf...)
}

// The crafted regression inputs. Each drove an unbounded allocation (or an
// unexercised failure path) in the pre-hardening reader.

// hugePayloadSample: the first data block claims a ~16 TiB large-mode payload.
func hugePayloadSample(t testing.TB) []byte {
	b := sampleBytes(t)
	binary.LittleEndian.PutUint16(b[sampleOffBlock1+2:], FlagCritical|FlagHasCRC|FlagLargeMode)
	binary.LittleEndian.PutUint32(b[sampleOffBlock1+4:], 0xFFFFFFFF)
	return b
}

// hugeBTTCountSample: the type table claims 2^32-1 entries (drove a >2 GiB alloc).
func hugeBTTCountSample(t testing.TB) []byte {
	b := sampleBytes(t)
	binary.LittleEndian.PutUint32(b[sampleOffBTTCount:], 0xFFFFFFFF)
	return b
}

// hugeNameLenSample: the writer name claims ~4 GiB.
func hugeNameLenSample(t testing.TB) []byte {
	b := sampleBytes(t)
	binary.LittleEndian.PutUint32(b[sampleOffNameLen:], 0xFFFFFFFF)
	return b
}

// corruptCRCSample: block 1's payload no longer matches its stored CRC.
func corruptCRCSample(t testing.TB) []byte {
	b := sampleBytes(t)
	b[sampleOffPayload1] ^= 0xFF
	return b
}

// nonSeeker hides Seek so the reader takes the capped, unknown-size path.
type nonSeeker struct{ r io.Reader }

func (n nonSeeker) Read(p []byte) (int, error) { return n.r.Read(p) }

// walkCount walks blocks with Next/SkipPayload until an error, returning the
// number of complete blocks and the terminating error.
func walkCount(br *Reader) (int, error) {
	n := 0
	for {
		b, err := br.Next()
		if err != nil {
			return n, err
		}
		if err := br.SkipPayload(b); err != nil {
			return n, err
		}
		n++
	}
}

func TestCRCMismatch(t *testing.T) {
	cases := map[string]int{
		"payload byte flipped": sampleOffPayload1,
		"crc byte flipped":     sampleOffCRC1,
	}
	for name, off := range cases {
		t.Run(name, func(t *testing.T) {
			b := sampleBytes(t)
			b[off] ^= 0x01
			br, err := NewReader(bytes.NewReader(b))
			require.NoError(t, err)
			blk, err := br.Next()
			require.NoError(t, err)
			_, err = br.ReadPayload(blk)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrCRCMismatch), "got %v", err)
		})
	}
}

func TestBadMagicTyped(t *testing.T) {
	b := sampleBytes(t)
	b[0] = 'X'
	_, err := NewReader(bytes.NewReader(b))
	assert.True(t, errors.Is(err, ErrBadMagic), "got %v", err)

	b = sampleBytes(t)
	b[8] = 0x00 // break the 0D 0A 1A 0A signature
	_, err = NewReader(bytes.NewReader(b))
	assert.True(t, errors.Is(err, ErrBadMagic), "got %v", err)
}

// TestHugePayloadClaimRejected: the ~16 TiB claim must be rejected before any
// allocation is sized from it (the pre-hardening reader allocated it).
func TestHugePayloadClaimRejected(t *testing.T) {
	br, err := NewReader(bytes.NewReader(hugePayloadSample(t)))
	require.NoError(t, err)

	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	_, err = br.Next()
	runtime.ReadMemStats(&m1)

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTruncated), "got %v", err)
	assert.Less(t, m1.TotalAlloc-m0.TotalAlloc, uint64(1<<20), "rejection must not allocate")
}

func TestHugeBTTCountRejected(t *testing.T) {
	_, err := NewReader(bytes.NewReader(hugeBTTCountSample(t)))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCapacityExceeded), "got %v", err)
}

func TestHugeWriterNameRejected(t *testing.T) {
	// seekable: bounded by the real input size
	_, err := NewReader(bytes.NewReader(hugeNameLenSample(t)))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTruncated), "got %v", err)

	// non-seekable: bounded by the allocation cap
	_, err = NewReader(nonSeeker{bytes.NewReader(hugeNameLenSample(t))})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCapacityExceeded), "got %v", err)
}

func TestBTTEntryNameOverrun(t *testing.T) {
	b := sampleBytes(t)
	binary.LittleEndian.PutUint32(b[sampleOffEntry0Name:], 0xFFFF) // 64 KiB name in a 112-byte table
	_, err := NewReader(bytes.NewReader(b))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCapacityExceeded), "got %v", err)
}

// TestNonSeekableCap: the NewReaderLimited cap protects streaming callers and
// is configurable; the default cap admits normal files.
func TestNonSeekableCap(t *testing.T) {
	// a tight cap rejects the sample's (valid) 112-byte block type table
	_, err := NewReaderLimited(nonSeeker{bytes.NewReader(sampleBytes(t))}, 64)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCapacityExceeded), "got %v", err)

	// the default cap admits the whole sample and the walk completes
	br, err := NewReader(nonSeeker{bytes.NewReader(sampleBytes(t))})
	require.NoError(t, err)
	assert.Equal(t, int64(-1), br.InputSize())
	n, werr := walkCount(br)
	assert.Equal(t, io.EOF, werr)
	assert.Equal(t, 3, n)
}

// TestBogusFileLengthCleanNoFooter: a lying file_length must degrade to a
// clean "no footer" result and a terminating walk -- never a wild seek.
func TestBogusFileLengthCleanNoFooter(t *testing.T) {
	for _, fl := range []uint64{296, 1 << 62, FileLengthUnknown - 1} {
		b := sampleBytes(t)
		binary.LittleEndian.PutUint64(b[sampleOffFileLength:], fl)
		br, err := NewReader(bytes.NewReader(b))
		require.NoError(t, err, "file_length=%d", fl)
		assert.False(t, br.HasIndex(), "file_length=%d", fl)
		n, werr := walkCount(br)
		assert.Equal(t, 3, n, "file_length=%d", fl)
		assert.True(t, werr == io.EOF || errors.Is(werr, ErrTruncated), "file_length=%d: got %v", fl, werr)
	}
}

// TestReadPayloadBoundsCallerBlock: even a caller-constructed Block cannot
// drive an unbounded allocation through ReadPayload.
func TestReadPayloadBoundsCallerBlock(t *testing.T) {
	br, err := NewReader(bytes.NewReader(sampleBytes(t)))
	require.NoError(t, err)
	_, err = br.ReadPayload(&Block{PayloadLen: 1 << 40})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTruncated), "got %v", err)
}

// TestTruncatedPrefixesTyped: cutting the sample mid-structure must always
// surface ErrTruncated (prefix lengths chosen to avoid clean block bounds).
func TestTruncatedPrefixesTyped(t *testing.T) {
	full := sampleBytes(t)
	for _, n := range []int{0, 4, 55, 58, 90, 150, 199, 219, 279} {
		err := readAllErr(full[:n])
		require.Error(t, err, "prefix %d", n)
		assert.True(t, errors.Is(err, ErrTruncated), "prefix %d: got %v", n, err)
	}
}

// readAllErr opens data and reads every block payload, returning the first error.
func readAllErr(data []byte) error {
	br, err := NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	for {
		b, err := br.Next()
		if err != nil {
			return err
		}
		if _, err := br.ReadPayload(b); err != nil {
			return err
		}
	}
}

// TestInputSize covers the seekable size probe.
func TestInputSize(t *testing.T) {
	br, err := NewReader(bytes.NewReader(sampleBytes(t)))
	require.NoError(t, err)
	assert.Equal(t, int64(280), br.InputSize())
}

// TestSeekSkipTruncationDetected: skip() seeks on seekable inputs, so make
// sure a file cut mid-block (after the payload the claim check admits, but
// before the block's aligned end) still reports ErrTruncated -- and does so
// identically to the non-seekable read-through path.
func TestSeekSkipTruncationDetected(t *testing.T) {
	// A streaming writer leaves file_length unknown, so the walk cannot stop
	// early on blocksEnd; the 3-byte payload needs 5 pad bytes we then cut.
	var buf bytes.Buffer
	bw, err := NewWriter(&buf, sampleWriter, "demo", sampleDefs)
	require.NoError(t, err)
	require.NoError(t, bw.Block(2, 0, []byte("abc")))
	require.NoError(t, bw.End())
	cut := buf.Bytes()[:buf.Len()-5]

	for name, r := range map[string]io.Reader{
		"seekable":     bytes.NewReader(cut),
		"non-seekable": nonSeeker{bytes.NewReader(cut)},
	} {
		t.Run(name, func(t *testing.T) {
			br, err := NewReader(r)
			require.NoError(t, err)
			b, err := br.Next()
			require.NoError(t, err) // the 3 payload bytes are all present...
			err = br.SkipPayload(b) // ...but the pad to the 8-byte boundary is not
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrTruncated), "got %v", err)
			assert.Equal(t, uint64(len(cut)), br.Offset(), "reader must stop at end of input")
		})
	}
}
