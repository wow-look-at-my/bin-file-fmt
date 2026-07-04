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
// footer up front; Next must recognize it by position (file_length-16) and
// end the walk cleanly instead of misparsing "rezapnib" as a block header.
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
