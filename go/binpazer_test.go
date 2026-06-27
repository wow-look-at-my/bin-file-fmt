package binpazer

import (
	"bytes"
	"hash/crc32"
	"io"
	"os"
	"testing"
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
	sampleDefs   = []TypeDef{{2, sampleMesh, "Mesh"}, {3, sampleNote, "Note"}}
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
	if err := bw.Block(2, FlagCritical|FlagHasCRC, []byte(meshPayload)); err != nil {
		return err
	}
	if err := bw.Block(3, 0, []byte(notePayload)); err != nil {
		return err
	}
	if err := bw.Block(2, FlagLargeMode, []byte(bigPayload)); err != nil {
		return err
	}
	return bw.End()
}

// verifySample reads a sample file and checks every field.
func verifySample(t *testing.T, r io.Reader) {
	t.Helper()
	br, err := NewReader(r)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if br.WriterName != "demo" {
		t.Errorf("writer name = %q", br.WriterName)
	}
	if br.VersionMajor != 1 || br.VersionMinor != 0 {
		t.Errorf("version = %d.%d", br.VersionMajor, br.VersionMinor)
	}
	if br.WriterGUID != sampleWriter {
		t.Errorf("writer guid mismatch")
	}
	if len(br.Types) != 3 {
		t.Fatalf("types = %d, want 3", len(br.Types))
	}
	if br.Types[0].TypeID != 1 || br.Types[0].GUID != BlockTypeTableGUID {
		t.Errorf("entry0 not BTT")
	}
	if br.Types[1].TypeID != 2 || br.Types[1].GUID != sampleMesh {
		t.Errorf("entry1 not Mesh")
	}
	if br.Types[2].TypeID != 3 || br.Types[2].GUID != sampleNote {
		t.Errorf("entry2 not Note")
	}

	want := []struct {
		typ     uint16
		flags   uint16
		guid    GUID
		payload string
	}{
		{2, FlagCritical | FlagHasCRC, sampleMesh, meshPayload},
		{3, 0, sampleNote, notePayload},
		{2, FlagLargeMode, sampleMesh, bigPayload},
	}
	for i, w := range want {
		b, err := br.Next()
		if err != nil {
			t.Fatalf("block %d Next: %v", i, err)
		}
		if b.TypeID != w.typ {
			t.Errorf("block %d type = %d, want %d", i, b.TypeID, w.typ)
		}
		if b.Flags != w.flags {
			t.Errorf("block %d flags = %#x, want %#x", i, b.Flags, w.flags)
		}
		if !b.Known || b.GUID != w.guid {
			t.Errorf("block %d guid not resolved", i)
		}
		if int(b.PayloadLen) != len(w.payload) {
			t.Errorf("block %d len = %d, want %d", i, b.PayloadLen, len(w.payload))
		}
		p, err := br.ReadPayload(b)
		if err != nil {
			t.Fatalf("block %d ReadPayload: %v", i, err)
		}
		if string(p) != w.payload {
			t.Errorf("block %d payload = %q, want %q", i, p, w.payload)
		}
	}
	if _, err := br.Next(); err != io.EOF {
		t.Errorf("expected io.EOF after last block, got %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	var m memFile
	if err := writeSample(&m); err != nil {
		t.Fatalf("writeSample: %v", err)
	}
	if len(m.buf) != 280 {
		t.Errorf("wrote %d bytes, want 280", len(m.buf))
	}
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
	if err := writeSample(&m); err != nil {
		t.Fatalf("writeSample: %v", err)
	}
	if !bytes.Equal(m.buf, fixture) {
		t.Errorf("Go output differs from C fixture (%d vs %d bytes)", len(m.buf), len(fixture))
	}
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
	if got := crc32.Checksum([]byte("123456789"), crcTable); got != 0xE3069283 {
		t.Errorf("CRC-32C(\"123456789\") = %#x, want 0xE3069283", got)
	}
}
