package binpazer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The appendable sample, identical to gen_appendable in c/gen.c.
var appendSamplePayloads = []string{"first record", "second record, a bit longer", ""}

func writeAppendSample(w io.Writer, end bool) (*Writer, error) {
	bw, err := NewAppendWriter(w, sampleWriter, "demo", sampleDefs[1:])
	if err != nil {
		return nil, err
	}
	for _, p := range appendSamplePayloads {
		if err := bw.Block(2, 0, []byte(p)); err != nil {
			return nil, err
		}
	}
	if end {
		if err := bw.End(); err != nil {
			return nil, err
		}
	}
	return bw, nil
}

func appendSampleBytes(t testing.TB, end bool) []byte {
	t.Helper()
	mf := &memFile{}
	_, err := writeAppendSample(mf, end)
	require.NoError(t, err)
	return mf.buf
}

// followAll reads committed blocks until the reader reports anything but a block.
func followAll(br *Reader) ([]string, error) {
	got := []string{}
	for {
		b, err := br.NextCommitted()
		if err != nil {
			return got, err
		}
		p, err := br.ReadPayload(b)
		if err != nil {
			return got, err
		}
		if b.TypeID != TypeEnd {
			got = append(got, string(p))
		}
	}
}

func TestAppendCrossImplWrite(t *testing.T) {
	want, err := os.ReadFile("testdata/sample-appendable.binpazer")
	require.NoError(t, err)
	assert.Equal(t, want, appendSampleBytes(t, true), "Go appendable output differs from the C fixture")
}

// The resume fixture of gen_resumed in c/gen.c: it pins the Padding bytes of a resume across implementations.
func TestResumeCrossImplWrite(t *testing.T) {
	want, err := os.ReadFile("testdata/sample-resumed.binpazer")
	require.NoError(t, err)

	torn := append([]byte{2, 0, byte(FlagHasCRC), 0, 0, 0, 0x10, 0}, bytes.Repeat([]byte{0xAB}, 13)...)
	mf := &memFile{buf: append(appendSampleBytes(t, false), torn...)}
	bw, _, err := ResumeAppendWriter(mf)
	require.NoError(t, err)
	require.NoError(t, bw.Block(2, 0, []byte("after the crash")))
	require.NoError(t, bw.End())
	assert.Equal(t, want, mf.buf, "Go resume output differs from the C fixture")
}

func TestAppendHeaderAndSeals(t *testing.T) {
	data := appendSampleBytes(t, false)
	br, err := NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	assert.True(t, br.Appendable())
	assert.EqualValues(t, VersionMinorAppendable, br.VersionMinor)
	assert.Equal(t, FileLengthUnknown, br.FileLength, "an open appendable file keeps the sentinel")
	assert.Equal(t, TypeEntry{TypeID: TypeEnd, GUID: EndGUID, Name: "End"}, br.Types[len(br.Types)-1])
	for {
		b, err := br.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		assert.NotZero(t, b.Flags&FlagHasCRC, "block at %d is not sealed", b.Offset())
		require.NoError(t, br.SkipPayload(b))
	}
	assert.False(t, br.Ended())
}

func TestAppendEndClosesTheFile(t *testing.T) {
	mf := &memFile{}
	bw, err := writeAppendSample(mf, true)
	require.NoError(t, err)
	assert.ErrorIs(t, bw.Block(2, 0, []byte("late")), ErrEnded)
	require.NoError(t, bw.End(), "End is idempotent")

	br, err := NewReader(bytes.NewReader(mf.buf))
	require.NoError(t, err)
	assert.EqualValues(t, len(mf.buf), br.FileLength, "a seekable End back-patches file_length")
	got, err := followAll(br)
	assert.Equal(t, io.EOF, err)
	assert.Equal(t, appendSamplePayloads, got)
	assert.True(t, br.Ended())
}

func TestAppendNonSeekableEndsInStream(t *testing.T) {
	var buf bytes.Buffer
	_, err := writeAppendSample(&buf, true)
	require.NoError(t, err)

	br, err := NewReader(nonSeeker{bytes.NewReader(buf.Bytes())})
	require.NoError(t, err)
	assert.Equal(t, FileLengthUnknown, br.FileLength)
	n := 0
	for {
		b, err := br.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		_, err = br.ReadPayload(b)
		require.NoError(t, err)
		n++
	}
	assert.Equal(t, len(appendSamplePayloads)+1, n, "the End block is a block like any other")
	assert.True(t, br.Ended(), "a stream reader learns of a clean close from the End block")
}

func TestAppendRefusesIndex(t *testing.T) {
	mf := &memFile{}
	bw, err := writeAppendSample(mf, false)
	require.NoError(t, err)
	require.Error(t, bw.WriteIndex())
	require.NoError(t, bw.Block(2, 0, []byte("still usable")), "a refused index must not poison the writer")
}

// blockEnds returns the offset past each top-level block of data.
func blockEnds(t testing.TB, data []byte) []uint64 {
	t.Helper()
	br, err := NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	var ends []uint64
	for {
		b, err := br.Next()
		if err == io.EOF {
			return ends
		}
		require.NoError(t, err)
		require.NoError(t, br.SkipPayload(b))
		ends = append(ends, br.Offset())
	}
}

// The property the format promises: at every length the file can have while it
// grows. A reader sees exactly the blocks that are wholly written, and nothing else.
func TestFollowIsSafeAtEveryPrefix(t *testing.T) {
	data := appendSampleBytes(t, true)
	ends := blockEnds(t, data)
	require.Len(t, ends, len(appendSamplePayloads)+1)

	br0, err := NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	headerEnd := br0.Offset()

	for n := headerEnd; n <= uint64(len(data)); n++ {
		br, err := NewReader(bytes.NewReader(data[:n]))
		require.NoError(t, err, "prefix %d", n)
		got, err := followAll(br)

		want := 0
		for _, e := range ends {
			if e <= n {
				want++
			}
		}
		if want == len(ends) {
			require.Equal(t, io.EOF, err, "prefix %d", n)
			want-- // followAll does not count the End block
		} else {
			require.ErrorIs(t, err, ErrPending, "prefix %d", n)
			require.NotErrorIs(t, err, ErrTorn, "prefix %d: a short file is in progress, not torn", n)
		}
		require.Equal(t, appendSamplePayloads[:want], got, "prefix %d", n)
	}
}

func TestFollowAGrowingFile(t *testing.T) {
	mf := &memFile{}
	bw, err := NewAppendWriter(mf, sampleWriter, "demo", sampleDefs)
	require.NoError(t, err)

	// The reader gets its own position over the same bytes, as a second process would.
	view := &sharedView{mf: mf}
	br, err := NewReader(view)
	require.NoError(t, err)

	_, err = br.NextCommitted()
	require.ErrorIs(t, err, ErrPending)
	at := br.Offset()
	_, err = br.NextCommitted()
	require.ErrorIs(t, err, ErrPending)
	assert.Equal(t, at, br.Offset(), "a pending reader does not move")

	require.NoError(t, bw.Block(1, 0, []byte("one")))
	b, err := br.NextCommitted()
	require.NoError(t, err)
	p, err := br.ReadPayload(b)
	require.NoError(t, err)
	assert.Equal(t, "one", string(p))
	assert.True(t, b.Known)

	_, err = br.NextCommitted()
	require.ErrorIs(t, err, ErrPending)

	big := bytes.Repeat([]byte("x"), 3*ioBufSize+5)
	require.NoError(t, bw.CompressedBlock(2, 0, CodecStored, big))
	b, err = br.NextCommitted()
	require.NoError(t, err)
	p, err = br.ReadDecompressedPayload(b)
	require.NoError(t, err)
	assert.Equal(t, big, p)

	require.NoError(t, bw.End())
	b, err = br.NextCommitted()
	require.NoError(t, err)
	assert.EqualValues(t, TypeEnd, b.TypeID)
	require.NoError(t, br.SkipPayload(b))
	_, err = br.NextCommitted()
	assert.Equal(t, io.EOF, err)
}

// sharedView reads a memFile through a position of its own.
type sharedView struct {
	mf  *memFile
	pos int64
}

func (v *sharedView) Read(p []byte) (int, error) {
	if v.pos >= int64(len(v.mf.buf)) {
		return 0, io.EOF
	}
	n := copy(p, v.mf.buf[v.pos:])
	v.pos += int64(n)
	return n, nil
}

func (v *sharedView) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		v.pos = off
	case io.SeekCurrent:
		v.pos += off
	case io.SeekEnd:
		v.pos = int64(len(v.mf.buf)) + off
	}
	return v.pos, nil
}

func TestFollowTornAndUnsealedBlocks(t *testing.T) {
	data := appendSampleBytes(t, false)
	ends := blockEnds(t, data)
	lastStart := ends[len(ends)-2]

	t.Run("bad seal", func(t *testing.T) {
		bad := bytes.Clone(data)
		bad[ends[0]+8] ^= 0xFF // a payload byte of the second record
		br, err := NewReader(bytes.NewReader(bad))
		require.NoError(t, err)
		got, err := followAll(br)
		require.ErrorIs(t, err, ErrTorn)
		require.ErrorIs(t, err, ErrPending, "a torn block is also not committed")
		assert.Equal(t, appendSamplePayloads[:1], got, "nothing from a torn block onward is surfaced")
		assert.Equal(t, ends[0], br.Offset(), "the reader stays at the start of the torn block")
	})
	t.Run("no seal", func(t *testing.T) {
		bad := bytes.Clone(data)
		flags := binary.LittleEndian.Uint16(bad[lastStart+2:])
		binary.LittleEndian.PutUint16(bad[lastStart+2:], flags&^FlagHasCRC)
		br, err := NewReader(bytes.NewReader(bad))
		require.NoError(t, err)
		got, err := followAll(br)
		require.ErrorIs(t, err, ErrTorn)
		assert.Equal(t, appendSamplePayloads[:2], got)
	})
	t.Run("zeroed space", func(t *testing.T) {
		padded := append(bytes.Clone(data), make([]byte, 64)...)
		br, err := NewReader(bytes.NewReader(padded))
		require.NoError(t, err)
		got, err := followAll(br)
		require.ErrorIs(t, err, ErrPending)
		require.NotErrorIs(t, err, ErrTorn)
		assert.Equal(t, appendSamplePayloads, got)
	})
}

func TestFollowNeedsAnAppendableSeekableFile(t *testing.T) {
	br, err := NewReader(bytes.NewReader(sampleBytes(t)))
	require.NoError(t, err)
	_, err = br.NextCommitted()
	assert.ErrorIs(t, err, ErrNotAppendable)

	br, err = NewReader(nonSeeker{bytes.NewReader(appendSampleBytes(t, false))})
	require.NoError(t, err)
	_, err = br.NextCommitted()
	assert.Error(t, err)
}

func TestFindListsCommittedBlocksOnly(t *testing.T) {
	data := appendSampleBytes(t, true)
	ends := blockEnds(t, data)
	// The file as it stood while the last record was part of the way in.
	open := data[:ends[2]-3]

	br, err := NewReader(bytes.NewReader(open))
	require.NoError(t, err)
	at := br.Offset()
	offs, err := br.Find(2)
	require.NoError(t, err)
	assert.Equal(t, []uint64{at, ends[0]}, offs, "the record still being written is not found")
	assert.Equal(t, at, br.Offset(), "Find keeps the reader's position")

	tbl, err := br.Find(TypeBlockTypeTable)
	require.NoError(t, err)
	assert.Equal(t, []uint64{br.FirstBlockOffset}, tbl)

	b, err := br.FindLast(2)
	require.NoError(t, err)
	p, err := br.ReadPayload(b)
	require.NoError(t, err)
	assert.Equal(t, appendSamplePayloads[1], string(p))

	br, err = NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	offs, err = br.Find(TypeEnd)
	require.NoError(t, err)
	assert.Equal(t, []uint64{ends[2]}, offs)
	assert.False(t, br.Ended(), "a Find that passes the End block does not end the caller's walk")
}

func TestResumeReportsTheSealedTail(t *testing.T) {
	clean := appendSampleBytes(t, false)
	bw, _, err := ResumeAppendWriter(&memFile{buf: bytes.Clone(clean)})
	require.NoError(t, err)
	assert.Zero(t, bw.SealedTail())

	// A tail that is a whole number of aligned words leaves the file length unchanged, so only the count tells.
	bw, _, err = ResumeAppendWriter(&memFile{buf: append(bytes.Clone(clean), bytes.Repeat([]byte{0xAB}, 24)...)})
	require.NoError(t, err)
	assert.EqualValues(t, 24, bw.SealedTail())
	assert.EqualValues(t, len(clean)+24, bw.Offset())
}

func TestResumeAppendsAfterCommittedBlocks(t *testing.T) {
	mf := &memFile{buf: appendSampleBytes(t, false)}
	before := len(mf.buf)

	bw, types, err := ResumeAppendWriter(mf)
	require.NoError(t, err)
	assert.EqualValues(t, before, bw.Offset(), "a clean file resumes at its end")
	assert.Len(t, mf.buf, before, "a clean file gets no padding")
	assert.Equal(t, "Note", types[1].Name)
	require.NoError(t, bw.Block(2, 0, []byte("after restart")))
	require.NoError(t, bw.End())

	br, err := NewReader(bytes.NewReader(mf.buf))
	require.NoError(t, err)
	got, err := followAll(br)
	assert.Equal(t, io.EOF, err)
	assert.Equal(t, append(append([]string{}, appendSamplePayloads...), "after restart"), got)
}

func TestResumeSealsATornTail(t *testing.T) {
	clean := appendSampleBytes(t, false)
	for _, tail := range []int{1, 7, 8, 9, 15, 16, 17, 100, 4096 + 3} {
		// A torn block: a header that promises more payload than the writer got to write.
		torn := make([]byte, tail)
		if tail >= 8 {
			binary.LittleEndian.PutUint16(torn[0:], 2)
			binary.LittleEndian.PutUint16(torn[2:], FlagHasCRC)
			binary.LittleEndian.PutUint32(torn[4:], 1<<20)
		}
		for i := 8; i < tail; i++ {
			torn[i] = 0xAB
		}
		mf := &memFile{buf: append(bytes.Clone(clean), torn...)}

		bw, _, err := ResumeAppendWriter(mf)
		require.NoError(t, err, "tail %d", tail)
		assert.GreaterOrEqual(t, bw.Offset(), uint64(len(clean)+tail), "tail %d: padding covers every stale byte", tail)
		assert.Zero(t, bw.Offset()%8)
		assert.Equal(t, clean, mf.buf[:len(clean)], "tail %d: committed bytes are untouched", tail)
		require.NoError(t, bw.Block(2, 0, []byte("recovered")))

		br, err := NewReader(bytes.NewReader(mf.buf))
		require.NoError(t, err)
		var got []string
		for {
			b, err := br.NextCommitted()
			if errors.Is(err, ErrPending) {
				require.NotErrorIs(t, err, ErrTorn, "tail %d", tail)
				break
			}
			require.NoError(t, err, "tail %d", tail)
			p, err := br.ReadPayload(b)
			require.NoError(t, err)
			if b.TypeID == TypePadding {
				assert.Equal(t, make([]byte, len(p)), p, "padding is zeros")
				continue
			}
			got = append(got, string(p))
		}
		assert.Equal(t, append(append([]string{}, appendSamplePayloads...), "recovered"), got, "tail %d", tail)
		assert.EqualValues(t, len(mf.buf), br.Offset(), "tail %d: no stale byte is left behind", tail)
	}
}

// A torn tail can hold a complete sealed block of its own. Resume must not let it surface.
func TestResumeHidesSealedBlocksInsideATornTail(t *testing.T) {
	clean := appendSampleBytes(t, false)

	inner := &memFile{}
	iw, err := NewAppendWriter(inner, sampleWriter, "demo", sampleDefs[1:])
	require.NoError(t, err)
	start := len(inner.buf)
	require.NoError(t, iw.Block(2, 0, []byte("ghost")))
	ghost := inner.buf[start:]

	// The torn block claims a long payload; a short first part and then the ghost made it to disk.
	torn := make([]byte, 8+16)
	binary.LittleEndian.PutUint16(torn[0:], 2)
	binary.LittleEndian.PutUint16(torn[2:], FlagHasCRC)
	binary.LittleEndian.PutUint32(torn[4:], 1<<20)
	mf := &memFile{buf: append(append(bytes.Clone(clean), torn...), ghost...)}

	bw, _, err := ResumeAppendWriter(mf)
	require.NoError(t, err)
	require.NoError(t, bw.Block(2, 0, []byte("real")))

	br, err := NewReader(bytes.NewReader(mf.buf))
	require.NoError(t, err)
	for {
		b, err := br.NextCommitted()
		if errors.Is(err, ErrPending) {
			break
		}
		require.NoError(t, err)
		p, err := br.ReadPayload(b)
		require.NoError(t, err)
		assert.NotEqual(t, "ghost", string(p))
	}
}

func TestResumeRefusals(t *testing.T) {
	_, _, err := ResumeAppendWriter(&memFile{buf: appendSampleBytes(t, true)})
	assert.ErrorIs(t, err, ErrEnded)

	_, _, err = ResumeAppendWriter(&memFile{buf: sampleBytes(t)})
	assert.ErrorIs(t, err, ErrNotAppendable)

	_, _, err = ResumeAppendWriter(&memFile{buf: bytes.Repeat([]byte("not binpazer "), 8)})
	assert.ErrorIs(t, err, ErrBadMagic)
}

func TestResumePadsALongTailInSeveralBlocks(t *testing.T) {
	clean := appendSampleBytes(t, false)
	tail := 2*maxPadExtent + 100
	mf := &memFile{buf: append(bytes.Clone(clean), bytes.Repeat([]byte{0xAB}, tail)...)}
	_, _, err := ResumeAppendWriter(mf)
	require.NoError(t, err)

	br, err := NewReader(bytes.NewReader(mf.buf))
	require.NoError(t, err)
	pads := 0
	for {
		b, err := br.NextCommitted()
		if errors.Is(err, ErrPending) {
			break
		}
		require.NoError(t, err)
		if b.TypeID == TypePadding {
			pads++
		}
		require.NoError(t, br.SkipPayload(b))
	}
	assert.Equal(t, 3, pads, "a tail longer than two Padding blocks takes three")
	assert.EqualValues(t, len(mf.buf), br.Offset())
	assert.GreaterOrEqual(t, len(mf.buf), len(clean)+tail)
}
