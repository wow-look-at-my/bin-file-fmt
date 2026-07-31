package binpazer

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	tFile uint16 = 1
	tDir  uint16 = 2
)

var containerDefs = []TypeDef{
	{TypeID: tFile, GUID: GUID{0x11}, Name: "File"},
	{TypeID: tDir, GUID: GUID{0x22}, Name: "Directory"},
}

type testDir struct {
	Entries map[string]uint64 `json:"entries"`
}

// buildContainer writes the shape these helpers exist for: content blocks, a
// JSON directory naming their offsets, then the index and footer.
func buildContainer(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var m memFile
	w, err := NewWriter(&m, GUID{1}, "container", containerDefs)
	require.NoError(t, err)

	dir := testDir{Entries: map[string]uint64{}}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		body, ok := files[name]
		if !ok {
			continue
		}
		off, err := w.PutCompressed(tFile, FlagHasCRC, CodecZstd, []byte(body))
		require.NoError(t, err)
		dir.Entries[name] = off
	}
	_, err = w.PutJSON(tDir, FlagCritical|FlagHasCRC, CodecZstd, dir)
	require.NoError(t, err)
	require.NoError(t, w.Finish())
	return m.buf
}

func TestContainerRoundTrip(t *testing.T) {
	files := map[string]string{"a.txt": "alpha", "b.txt": "beta", "c.txt": "gamma"}
	raw := buildContainer(t, files)

	br, err := NewReaderAt(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err)

	var dir testDir
	require.NoError(t, br.DecodeJSONLast(tDir, &dir))
	assert.Len(t, dir.Entries, 3)

	for name, want := range files {
		// A fresh reader per lookup, the way concurrent readers share one blob.
		r2, err := NewReaderAt(bytes.NewReader(raw), int64(len(raw)))
		require.NoError(t, err)
		rd, b, err := r2.OpenAt(dir.Entries[name], tFile)
		require.NoError(t, err, name)
		assert.Equal(t, tFile, b.TypeID)
		got, err := io.ReadAll(rd)
		require.NoError(t, err)
		assert.Equal(t, want, string(got), name)
	}
}

// Finish must produce the same bytes as WriteIndex followed by End -- it is a
// shorthand, not a different file.
func TestFinishMatchesWriteIndexThenEnd(t *testing.T) {
	write := func(finish bool) []byte {
		var m memFile
		w, err := NewWriter(&m, GUID{1}, "container", containerDefs)
		require.NoError(t, err)
		_, err = w.Put(tFile, FlagHasCRC, []byte("payload"))
		require.NoError(t, err)
		if finish {
			require.NoError(t, w.Finish())
		} else {
			require.NoError(t, w.WriteIndex())
			require.NoError(t, w.End())
		}
		return m.buf
	}
	assert.Equal(t, write(false), write(true))
}

// Put and PutCompressed must report the offset the block actually landed at,
// which is the whole reason they exist.
func TestPutReportsTheRealOffset(t *testing.T) {
	var m memFile
	w, err := NewWriter(&m, GUID{1}, "container", containerDefs)
	require.NoError(t, err)

	plainOff, err := w.Put(tFile, FlagHasCRC, []byte("plain"))
	require.NoError(t, err)
	compOff, err := w.PutCompressed(tFile, FlagHasCRC, CodecZstd, []byte("compressed"))
	require.NoError(t, err)
	require.NoError(t, w.Finish())

	br, err := NewReaderAt(bytes.NewReader(m.buf), int64(len(m.buf)))
	require.NoError(t, err)
	b, err := br.BlockAt(plainOff, tFile)
	require.NoError(t, err)
	payload, err := br.ReadDecompressedPayload(b)
	require.NoError(t, err)
	assert.Equal(t, "plain", string(payload))

	br2, err := NewReaderAt(bytes.NewReader(m.buf), int64(len(m.buf)))
	require.NoError(t, err)
	b2, err := br2.BlockAt(compOff, tFile)
	require.NoError(t, err)
	assert.NotZero(t, b2.Flags&FlagCompressed)
	payload2, err := br2.ReadDecompressedPayload(b2)
	require.NoError(t, err)
	assert.Equal(t, "compressed", string(payload2))
}

// FindLast is what a rewritten directory relies on: the newest block of a type
// wins over the ones it replaced.
func TestFindFirstAndLast(t *testing.T) {
	var m memFile
	w, err := NewWriter(&m, GUID{1}, "container", containerDefs)
	require.NoError(t, err)
	_, err = w.PutJSON(tDir, 0, CodecStored, testDir{Entries: map[string]uint64{"old": 1}})
	require.NoError(t, err)
	_, err = w.PutJSON(tDir, 0, CodecStored, testDir{Entries: map[string]uint64{"new": 2}})
	require.NoError(t, err)
	require.NoError(t, w.Finish())

	br, err := NewReaderAt(bytes.NewReader(m.buf), int64(len(m.buf)))
	require.NoError(t, err)
	var newest testDir
	require.NoError(t, br.DecodeJSONLast(tDir, &newest))
	assert.Equal(t, map[string]uint64{"new": 2}, newest.Entries)

	br2, err := NewReaderAt(bytes.NewReader(m.buf), int64(len(m.buf)))
	require.NoError(t, err)
	first, err := br2.FindFirst(tDir)
	require.NoError(t, err)
	var oldest testDir
	require.NoError(t, br2.DecodeJSON(first, &oldest))
	assert.Equal(t, map[string]uint64{"old": 1}, oldest.Entries)
}

func TestFindMissingTypeIsTyped(t *testing.T) {
	raw := buildContainer(t, map[string]string{"a.txt": "alpha"})
	br, err := NewReaderAt(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err)

	_, err = br.FindLast(999)
	require.ErrorIs(t, err, ErrBlockNotFound)
	_, err = br.FindFirst(999)
	require.ErrorIs(t, err, ErrBlockNotFound)
	require.ErrorIs(t, br.DecodeJSONLast(999, &testDir{}), ErrBlockNotFound)
}

// A directory pointing at the wrong block is the file disagreeing with itself,
// and must not be reported as a missing block or decoded as the wrong type.
func TestBlockAtWrongTypeIsTyped(t *testing.T) {
	files := map[string]string{"a.txt": "alpha"}
	raw := buildContainer(t, files)

	br, err := NewReaderAt(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err)
	dirBlock, err := br.FindLast(tDir)
	require.NoError(t, err)

	br2, err := NewReaderAt(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err)
	_, err = br2.BlockAt(dirBlock.Offset(), tFile)
	require.ErrorIs(t, err, ErrWrongBlockType)

	br3, err := NewReaderAt(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err)
	_, _, err = br3.OpenAt(dirBlock.Offset(), tFile)
	require.ErrorIs(t, err, ErrWrongBlockType)
}

func TestDecodeJSONRejectsNonJSONPayload(t *testing.T) {
	var m memFile
	w, err := NewWriter(&m, GUID{1}, "container", containerDefs)
	require.NoError(t, err)
	_, err = w.PutCompressed(tDir, FlagHasCRC, CodecZstd, []byte{0x00, 0x01, 0x02})
	require.NoError(t, err)
	require.NoError(t, w.Finish())

	br, err := NewReaderAt(bytes.NewReader(m.buf), int64(len(m.buf)))
	require.NoError(t, err)
	var dir testDir
	require.Error(t, br.DecodeJSONLast(tDir, &dir))
}

func TestPutJSONRejectsUnmarshalableValue(t *testing.T) {
	var m memFile
	w, err := NewWriter(&m, GUID{1}, "container", containerDefs)
	require.NoError(t, err)
	_, err = w.PutJSON(tDir, 0, CodecStored, make(chan int))
	require.Error(t, err)
}
