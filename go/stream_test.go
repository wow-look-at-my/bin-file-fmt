package binpazer

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// indexedSample writes a small file with a Block Index and footer.
func indexedSample(t *testing.T) []byte {
	t.Helper()
	var m memFile
	bw, err := NewWriter(&m, sampleWriter, "demo", sampleDefs)
	require.NoError(t, err)
	require.NoError(t, bw.Block(1, FlagHasCRC, []byte(meshPayload)))
	require.NoError(t, bw.Block(2, 0, []byte(notePayload)))
	require.NoError(t, bw.WriteIndex())
	require.NoError(t, bw.End())
	return append([]byte(nil), m.buf...)
}

// TestStreamWalkStopsAtFooter: a non-seekable reader cannot exclude the
// footer up front. This also covers Next must recognize it by position
// (file_length-16) and end the walk cleanly instead of misparsing
// "rezapnib" as a block header.
func TestStreamWalkStopsAtFooter(t *testing.T) {
	data := indexedSample(t)

	countBlocks := func(br *Reader) int {
		n := 0
		for {
			b, err := br.Next()
			if err == io.EOF {
				return n
			}
			require.NoError(t, err)
			require.NoError(t, br.SkipPayload(b))
			n++
		}
	}

	seekable, err := NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	wantBlocks := countBlocks(seekable) // data blocks + the index block

	stream, err := NewReader(nonSeeker{bytes.NewReader(data)})
	require.NoError(t, err)
	assert.False(t, stream.HasIndex(), "footer not probed without a seeker")
	assert.Equal(t, wantBlocks, countBlocks(stream))
	assert.True(t, stream.HasIndex(), "footer recorded when the walk reached it")
	assert.Equal(t, uint64(len(data)), stream.Offset(), "footer consumed")
}

// TestStreamWalkNoFooter: the peek must not fire on files without a footer.
func TestStreamWalkNoFooter(t *testing.T) {
	data := sampleBytes(t)
	stream, err := NewReader(nonSeeker{bytes.NewReader(data)})
	require.NoError(t, err)
	n := 0
	for {
		b, err := stream.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		require.NoError(t, stream.SkipPayload(b))
		n++
	}
	assert.Equal(t, 3, n)
	assert.False(t, stream.HasIndex())
}

// largestRead records the largest read the reader asks its input for.
type largestRead struct {
	*bytes.Reader
	max int
}

func (l *largestRead) Read(p []byte) (int, error) {
	if len(p) > l.max {
		l.max = len(p)
	}
	return l.Reader.Read(p)
}

// TestSeekKeepsFirstReadSmall: a lookup by offset asks the input for at most
// seekReadAhead bytes, where a plain buffer refill asks for the whole buffer.
func TestSeekKeepsFirstReadSmall(t *testing.T) {
	payload := bytes.Repeat([]byte("p"), 100)
	const blocks = 2000
	file := writeTo(t, compressDefs, func(w *Writer) {
		for i := 0; i < blocks; i++ {
			require.NoError(t, w.Block(1, FlagHasCRC, payload))
		}
	})
	require.Greater(t, len(file), 2*ioBufSize)

	in := &largestRead{Reader: bytes.NewReader(file)}
	r, err := NewReader(in)
	require.NoError(t, err)
	offs, err := r.Find(1)
	require.NoError(t, err)
	require.Len(t, offs, blocks)

	in.max = 0
	b, err := r.At(offs[blocks/2])
	require.NoError(t, err)
	got, err := r.ReadPayload(b)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	assert.LessOrEqual(t, in.max, seekReadAhead)

	// Reading on from there goes back to full refills.
	in.max = 0
	for i := 0; i < 200; i++ {
		b, err = r.Next()
		require.NoError(t, err)
		got, err = r.ReadPayload(b)
		require.NoError(t, err)
		require.Equal(t, payload, got)
	}
	assert.Greater(t, in.max, seekReadAhead)
}
