package binpazer

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memFile is an in-memory io.Writer + io.Seeker + io.Reader (no real file IO).
type memFile struct {
	buf []byte
	pos int64
}

func (m *memFile) grow(end int64) {
	if end > int64(len(m.buf)) {
		m.buf = append(m.buf, make([]byte, end-int64(len(m.buf)))...)
	}
}
func (m *memFile) Write(p []byte) (int, error) {
	m.grow(m.pos + int64(len(p)))
	copy(m.buf[m.pos:], p)
	m.pos += int64(len(p))
	return len(p), nil
}
func (m *memFile) Seek(off int64, whence int) (int64, error) {
	if whence != io.SeekStart {
		panic("only SeekStart used")
	}
	m.pos = off
	m.grow(m.pos)
	return m.pos, nil
}
func (m *memFile) Read(p []byte) (int, error) {
	if m.pos >= int64(len(m.buf)) {
		return 0, io.EOF
	}
	n := copy(p, m.buf[m.pos:])
	m.pos += int64(n)
	return n, nil
}

// The canonical sample, identical to c/gen.c, so output is byte-comparable.
var (
	sampleWriter = GUID{0xaa, 0xbb, 0xcc, 0xdd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	sampleMesh   = GUID{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00}
	sampleNote   = GUID{0xde, 0xad, 0xbe, 0xef, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	sampleDefs   = []TypeDef{{1, sampleMesh, "Mesh"}, {2, sampleNote, "Note"}}
)

const (
	meshPayload = "vertices-go-here"         // 16
	notePayload = "hello, file!"             // 12
	bigPayload  = "0123456789ABCDEF01234567" // 24 (multiple of 4)
)

func writeSample(w io.Writer) error {
	bw, err := NewWriter(w, sampleWriter, "demo", sampleDefs)
	if err != nil {
		return err
	}
	if err := bw.Block(1, FlagCritical|FlagHasCRC, []byte(meshPayload)); err != nil {
		return err
	}
	if err := bw.Block(2, 0, []byte(notePayload)); err != nil {
		return err
	}
	if err := bw.Block(1, FlagLargeMode, []byte(bigPayload)); err != nil {
		return err
	}
	return bw.End()
}

// verifySample reads a sample file and checks every field.
func verifySample(t *testing.T, r io.Reader) {
	t.Helper()
	br, err := NewReader(r)
	require.Nil(t, err)

	assert.Equal(t, "demo", br.WriterName)

	assert.False(t, br.VersionMajor != 1 || br.VersionMinor != 0)

	assert.Equal(t, sampleWriter, br.WriterGUID)

	require.Equal(t, 3, len(br.Types))

	assert.False(t, br.Types[0].TypeID != TypeBlockTypeTable || br.Types[0].GUID != BlockTypeTableGUID)

	assert.False(t, br.Types[1].TypeID != 1 || br.Types[1].GUID != sampleMesh)

	assert.False(t, br.Types[2].TypeID != 2 || br.Types[2].GUID != sampleNote)

	want := []struct {
		typ     uint16
		flags   uint16
		guid    GUID
		payload string
	}{
		{1, FlagCritical | FlagHasCRC, sampleMesh, meshPayload},
		{2, 0, sampleNote, notePayload},
		{1, FlagLargeMode, sampleMesh, bigPayload},
	}
	for _, w := range want {
		b, err := br.Next()
		require.Nil(t, err)

		assert.Equal(t, w.typ, b.TypeID)

		assert.Equal(t, w.flags, b.Flags)

		assert.False(t, !b.Known || b.GUID != w.guid)

		assert.Equal(t, len(w.payload), int(b.PayloadLen))

		p, err := br.ReadPayload(b)
		require.Nil(t, err)

		assert.Equal(t, w.payload, string(p))

	}
	_, err = br.Next()
	assert.Equal(t, io.EOF, err)

}

func TestRoundTrip(t *testing.T) {
	var m memFile
	require.NoError(t, writeSample(&m))

	assert.Equal(t, 280, len(m.buf))

	m.pos = 0
	verifySample(t, &m)
}

// TestCrossImplWrite: Go output must be byte-identical to the C-written fixture.
func TestCrossImplWrite(t *testing.T) {
	fixture, err := os.ReadFile("testdata/sample.binpazer")
	if err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	var m memFile
	require.NoError(t, writeSample(&m))

	assert.True(t, bytes.Equal(m.buf, fixture))

}

// TestCrossImplRead: Go must read the C-written fixture.
func TestCrossImplRead(t *testing.T) {
	f, err := os.Open("testdata/sample.binpazer")
	if err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	defer f.Close()
	verifySample(t, f)
}

// TestCRC32C confirms the table is standard CRC-32C (matching the C impl).
func TestCRC32C(t *testing.T) {
	got := crc32.Checksum([]byte("123456789"), crcTable)
	assert.Equal(t, uint32(0xE3069283), got)

}

// TestSkipPayload exercises skipping a block's payload instead of reading it.
func TestSkipPayload(t *testing.T) {
	var m memFile
	require.NoError(t, writeSample(&m))
	m.pos = 0

	br, err := NewReader(&m)
	require.NoError(t, err)

	b, err := br.Next() // mesh
	require.NoError(t, err)
	require.NoError(t, br.SkipPayload(b))

	b, err = br.Next() // note
	require.NoError(t, err)
	p, err := br.ReadPayload(b)
	require.NoError(t, err)
	assert.Equal(t, notePayload, string(p))

	b, err = br.Next() // large
	require.NoError(t, err)
	require.NoError(t, br.SkipPayload(b))

	_, err = br.Next()
	assert.Equal(t, io.EOF, err)
}

// TestNoSeekStreaming covers a non-seekable writer: file_length stays the
// streaming sentinel and the reader terminates on EOF instead.
func TestNoSeekStreaming(t *testing.T) {
	var buf bytes.Buffer // bytes.Buffer is not an io.Seeker
	bw, err := NewWriter(&buf, sampleWriter, "demo", sampleDefs)
	require.NoError(t, err)
	require.NoError(t, bw.Block(2, 0, []byte(notePayload)))
	require.NoError(t, bw.End())

	assert.Equal(t, FileLengthUnknown, binary.LittleEndian.Uint64(buf.Bytes()[32:40]))

	br, err := NewReader(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	assert.Equal(t, FileLengthUnknown, br.FileLength)

	b, err := br.Next()
	require.NoError(t, err)
	assert.Equal(t, uint16(2), b.TypeID)
	p, err := br.ReadPayload(b)
	require.NoError(t, err)
	assert.Equal(t, notePayload, string(p))

	_, err = br.Next()
	assert.Equal(t, io.EOF, err)
}

// TestLargeModeRequiresMultipleOf4 covers the large_mode validation path.
func TestLargeModeRequiresMultipleOf4(t *testing.T) {
	var m memFile
	bw, err := NewWriter(&m, sampleWriter, "demo", sampleDefs)
	require.NoError(t, err)
	require.Error(t, bw.Block(1, FlagLargeMode, []byte("xyz"))) // 3 bytes
}
