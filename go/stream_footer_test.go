package binpazer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// streamedIndexed writes a file with a Block Index to a sink that cannot seek, so file_length stays unknown.
func streamedIndexed(t *testing.T) ([]byte, [][]byte) {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriterVersion(&buf, GUID{1}, "x", compressDefs, VersionMinorCompression)
	if err != nil {
		t.Fatal(err)
	}
	payloads := [][]byte{bytes.Repeat([]byte("streamed payload "), 64), []byte("raw tail")}
	if err := w.CompressedBlock(1, FlagHasCRC, CodecZstd, payloads[0]); err != nil {
		t.Fatal(err)
	}
	if err := w.Block(1, FlagHasCRC, payloads[1]); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteIndex(); err != nil {
		t.Fatal(err)
	}
	if err := w.End(); err != nil {
		t.Fatal(err)
	}
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
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		p, err := br.ReadDecompressedPayload(b)
		if err != nil {
			t.Fatalf("payload of block %d: %v", b.TypeID, err)
		}
		if b.TypeID == 1 {
			got = append(got, p)
		}
	}
}

func TestStreamedIndexReadsBack(t *testing.T) {
	file, payloads := streamedIndexed(t)
	if binary.LittleEndian.Uint64(file[32:]) != FileLengthUnknown {
		t.Fatal("file_length is not the streaming sentinel")
	}
	wantIdx := binary.LittleEndian.Uint64(file[len(file)-16:])
	if binary.LittleEndian.Uint16(file[wantIdx:]) != TypeBlockIndex {
		t.Fatal("the footer does not point at the Block Index")
	}

	for _, tc := range []struct {
		name string
		src  io.Reader
	}{
		{"non-seekable", bytes.NewBuffer(file)},
		{"seekable", bytes.NewReader(file)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			br, err := NewReader(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			got := walkAll(t, br)
			if len(got) != 2 || !bytes.Equal(got[0], payloads[0]) || !bytes.Equal(got[1], payloads[1]) {
				t.Fatalf("payloads do not round-trip: got %d blocks", len(got))
			}
			idx, ok := br.IndexOffset()
			if !ok || idx != wantIdx {
				t.Fatalf("IndexOffset = %d, %v; want %d, true", idx, ok, wantIdx)
			}
			if br.Offset() != uint64(len(file)) {
				t.Fatalf("walk ended at %d, want %d (after the footer)", br.Offset(), len(file))
			}
		})
	}

	br, err := NewReader(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	if !br.HasIndex() {
		t.Fatal("the seekable reader did not find the footer before the walk")
	}
	offs, err := br.Find(1)
	if err != nil || len(offs) != 2 {
		t.Fatalf("Find(1) = %v, %v; want two offsets from the index", offs, err)
	}
}

// A streamed file can end in a 16-byte block whose payload is the footer magic. It must stay a block.
func TestStreamedMagicPayloadIsNotAFooter(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, GUID{1}, "x", compressDefs)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Block(1, 0, []byte(footerMagic)); err != nil {
		t.Fatal(err)
	}
	if err := w.End(); err != nil {
		t.Fatal(err)
	}
	file := buf.Bytes()
	for _, src := range []io.Reader{bytes.NewBuffer(file), bytes.NewReader(file)} {
		br, err := NewReader(src)
		if err != nil {
			t.Fatal(err)
		}
		got := walkAll(t, br)
		if len(got) != 1 || string(got[0]) != footerMagic {
			t.Fatalf("the magic payload block did not read back: %q", got)
		}
		if br.HasIndex() {
			t.Fatal("a block payload was taken for a footer")
		}
	}
}
