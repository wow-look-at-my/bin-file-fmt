package binpazer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/stretchr/testify/require"
	"io"
	"testing"
)

// streamedIndexed writes a file with a Block Index to a sink that cannot seek, so file_length stays unknown.
func streamedIndexed(t *testing.T) ([]byte, [][]byte) {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriterVersion(&buf, GUID{1}, "x", compressDefs, VersionMinorCompression)
	require.NoError(t, err)

	payloads := [][]byte{bytes.Repeat([]byte("streamed payload "), 64), []byte("raw tail")}
	require.NoError(t, w.CompressedBlock(1, FlagHasCRC, CodecZstd, payloads[0]))

	require.NoError(t, w.Block(1, FlagHasCRC, payloads[1]))

	require.NoError(t, w.WriteIndex())

	require.NoError(t, w.End())

	return buf.Bytes(), payloads
}

// walkAll reads every block, decoding it, and returns the data blocks (one type).
func walkAll(t *testing.T, br *Reader) [][]byte {
	t.Helper()
	var got [][]byte
	for {
		b, err := br.Next()
		if errors.Is(err, io.EOF) {
			return got
		}
		require.NoError(t, err)

		p, err := br.ReadDecompressedPayload(b)
		require.NoError(t, err)

		if b.TypeID == 1 {
			got = append(got, p)
		}
	}
}

func TestStreamedIndexReadsBack(t *testing.T) {
	file, payloads := streamedIndexed(t)
	require.Equal(t, FileLengthUnknown, binary.LittleEndian.Uint64(file[32:]))

	wantIdx := binary.LittleEndian.Uint64(file[len(file)-16:])
	require.Equal(t, uint16(TypeBlockIndex), binary.LittleEndian.Uint16(file[wantIdx:]))

	for _, tc := range []struct {
		name string
		src  io.Reader
		end  int // a seekable walk stops before the footer, a streamed walk reads it
	}{
		{"non-seekable", bytes.NewBuffer(file), len(file)},
		{"seekable", bytes.NewReader(file), len(file) - 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			br, err := NewReader(tc.src)
			require.NoError(t, err)

			got := walkAll(t, br)
			require.False(t, len(got) != 2 || !bytes.Equal(got[0], payloads[0]) || !bytes.Equal(got[1], payloads[1]))

			idx, ok := br.IndexOffset()
			require.False(t, !ok || idx != wantIdx)

			require.Equal(t, uint64(tc.end), br.Offset())

		})
	}

	br, err := NewReader(bytes.NewReader(file))
	require.NoError(t, err)

	require.True(t, br.HasIndex())

	offs, err := br.Find(1)
	require.False(t, err != nil || len(offs) != 2)

}

// A streamed file can end in a 16-byte block whose payload is the footer magic. It must stay a block.
func TestStreamedMagicPayloadIsNotAFooter(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, GUID{1}, "x", compressDefs)
	require.NoError(t, err)

	require.NoError(t, w.Block(1, 0, []byte(footerMagic)))

	require.NoError(t, w.End())

	file := buf.Bytes()
	for _, src := range []io.Reader{bytes.NewBuffer(file), bytes.NewReader(file)} {
		br, err := NewReader(src)
		require.NoError(t, err)

		got := walkAll(t, br)
		require.False(t, len(got) != 1 || string(got[0]) != footerMagic)

		require.False(t, br.HasIndex())

	}
}
