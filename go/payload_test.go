package binpazer

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWritePayloadTo streams every sample block and expects the exact bytes
// ReadPayload would have returned, including CRC verification on one block.
func TestWritePayloadTo(t *testing.T) {
	br, err := NewReader(bytes.NewReader(sampleBytes(t)))
	require.NoError(t, err)

	for _, want := range []string{meshPayload, notePayload, bigPayload} {
		b, err := br.Next()
		require.NoError(t, err)
		var buf bytes.Buffer
		n, err := br.WritePayloadTo(b, &buf)
		require.NoError(t, err)
		assert.Equal(t, int64(len(want)), n)
		assert.Equal(t, want, buf.String())
	}
	_, err = br.Next()
	assert.Equal(t, io.EOF, err)
}

// TestWritePayloadToCRCMismatch: a corrupt payload must surface
// ErrCRCMismatch from the streaming path too.
func TestWritePayloadToCRCMismatch(t *testing.T) {
	br, err := NewReader(bytes.NewReader(corruptCRCSample(t)))
	require.NoError(t, err)
	b, err := br.Next()
	require.NoError(t, err)
	_, err = br.WritePayloadTo(b, io.Discard)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCRCMismatch), "got %v", err)
}

func TestWritePayloadToMultiChunk(t *testing.T) {
	payload := make([]byte, ioBufSize*3+12345)
	for i := range payload {
		payload[i] = byte(i * 31)
	}
	var m memFile
	bw, err := NewWriter(&m, sampleWriter, "demo", sampleDefs)
	require.NoError(t, err)
	require.NoError(t, bw.Block(1, FlagHasCRC, payload))
	require.NoError(t, bw.Block(2, FlagHasCRC, nil)) // empty payload, CRC of header only
	require.NoError(t, bw.End())

	m.pos = 0
	br, err := NewReader(&m)
	require.NoError(t, err)

	b, err := br.Next()
	require.NoError(t, err)
	var buf bytes.Buffer
	n, err := br.WritePayloadTo(b, &buf)
	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), n)
	assert.True(t, bytes.Equal(payload, buf.Bytes()))

	b, err = br.Next()
	require.NoError(t, err)
	n, err = br.WritePayloadTo(b, io.Discard)
	require.NoError(t, err)
	assert.Zero(t, n)

	_, err = br.Next()
	assert.Equal(t, io.EOF, err)
}

// TestPayloadReader covers full consumption, partial consumption followed by
// SkipPayload (which must land on the next block), and clean EOF semantics.
func TestPayloadReader(t *testing.T) {
	br, err := NewReader(bytes.NewReader(sampleBytes(t)))
	require.NoError(t, err)

	b, err := br.Next()
	require.NoError(t, err)
	pr, err := br.PayloadReader(b)
	require.NoError(t, err)
	head := make([]byte, 4)
	_, err = io.ReadFull(pr, head)
	require.NoError(t, err)
	assert.Equal(t, meshPayload[:4], string(head))
	require.NoError(t, br.SkipPayload(b)) // remainder + CRC + pad

	b, err = br.Next()
	require.NoError(t, err)
	pr, err = br.PayloadReader(b)
	require.NoError(t, err)
	all, err := io.ReadAll(pr)
	require.NoError(t, err)
	assert.Equal(t, notePayload, string(all))
	var one [1]byte
	_, err = pr.Read(one[:])
	assert.Equal(t, io.EOF, err)
	require.NoError(t, br.SkipPayload(b))

	b, err = br.Next()
	require.NoError(t, err)
	assert.Equal(t, uint64(len(bigPayload)), b.PayloadLen)
	p, err := br.ReadPayload(b)
	require.NoError(t, err)
	assert.Equal(t, bigPayload, string(p))
}

// TestPayloadReaderTruncated: an input cut mid-payload must report
// ErrTruncated through the payload reader.
func TestPayloadReaderTruncated(t *testing.T) {
	full := sampleBytes(t)
	cut := full[:sampleOffPayload1+4]
	br, err := NewReader(nonSeeker{bytes.NewReader(cut)})
	require.NoError(t, err)
	b, err := br.Next()
	require.NoError(t, err)
	pr, err := br.PayloadReader(b)
	require.NoError(t, err)
	_, err = io.ReadAll(pr)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTruncated), "got %v", err)
}
