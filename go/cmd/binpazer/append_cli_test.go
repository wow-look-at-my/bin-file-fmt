package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// writeAppendManifest writes a manifest for an appendable file with records and returns its path.
func writeAppendManifest(t *testing.T, dir string, open bool) string {
	t.Helper()
	m := &Manifest{
		WriterGUID: "aabbccdd-0000-0000-0000-000000000001",
		WriterName: "demo",
		Types:      []ManifestType{{TypeID: 2, GUID: "deadbeef-0000-0000-0000-000000000000", Name: "Note"}},
		Blocks: []ManifestBlock{
			{TypeID: 2, Data: "first record"},
			{TypeID: 2, Codec: "zstd", Data: strings.Repeat("second record ", 40)},
		},
		Appendable: true,
		Open:       open,
	}
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	p := filepath.Join(dir, "manifest.json")
	require.NoError(t, os.WriteFile(p, raw, 0o644))
	return p
}

// packAppendable packs the appendable manifest and returns the archive path.
func packAppendable(t *testing.T, open bool) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "log.binpazer")
	_, err := execute("pack", writeAppendManifest(t, dir, open), "-o", out)
	require.NoError(t, err)
	return out
}

// followLines parses follow output into its JSON lines.
func followLines(t *testing.T, out string) []followLine {
	t.Helper()
	var lines []followLine
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "" {
			continue
		}
		var fl followLine
		require.NoError(t, json.Unmarshal([]byte(l), &fl), "line %q", l)
		lines = append(lines, fl)
	}
	return lines
}

func payloadOf(t *testing.T, fl followLine) string {
	t.Helper()
	require.NotNil(t, fl.PayloadB64)
	p, err := base64.StdEncoding.DecodeString(*fl.PayloadB64)
	require.NoError(t, err)
	return string(p)
}

func appendBytes(t *testing.T, path string, extra []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)
	_, err = f.Write(extra)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

// sealedBlock returns the bytes of one sealed block, as an appendable writer lays it out.
func sealedBlock(typeID uint16, payload []byte) []byte {
	b := make([]byte, 8, 8+len(payload)+12)
	binary.LittleEndian.PutUint16(b[0:], typeID)
	binary.LittleEndian.PutUint16(b[2:], binpazer.FlagHasCRC)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(payload)))
	b = append(b, payload...)
	b = binary.LittleEndian.AppendUint32(b, crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli)))
	for len(b)%8 != 0 {
		b = append(b, 0)
	}
	return b
}

func TestAppendableLifecycle(t *testing.T) {
	p := packAppendable(t, true)

	out, err := execute("info", p)
	require.NoError(t, err)
	assert.Contains(t, out, "binpazer v1.2")
	assert.Contains(t, out, "appendable:  open\n")

	out, err = execute("follow", p, "--no-wait")
	require.NoError(t, err)
	lines := followLines(t, out)
	require.Len(t, lines, 2)
	assert.Equal(t, "first record", payloadOf(t, lines[0]))
	assert.Equal(t, strings.Repeat("second record ", 40), payloadOf(t, lines[1]), "a compressed payload is decoded")
	assert.Equal(t, "zstd", lines[1].Codec)
	assert.Equal(t, "Note", lines[0].TypeName)
	assert.Contains(t, lines[0].Flags, "has_crc")

	out, err = execute("append", p, "--type", "2", "--data", "third record", "--flags", "critical")
	require.NoError(t, err)
	assert.Contains(t, out, "appended a type 2 block at offset")
	stdout, _, err := executeIO(strings.NewReader("from stdin"), "append", p, "--type", "2", "--payload", "-", "--codec", "lz4")
	require.NoError(t, err, stdout)

	out, err = execute("follow", p, "--no-wait", "--no-payload")
	require.NoError(t, err)
	lines = followLines(t, out)
	require.Len(t, lines, 4)
	assert.Nil(t, lines[2].PayloadB64)
	assert.Contains(t, lines[2].Flags, "critical")
	assert.Equal(t, "lz4", lines[3].Codec)

	out, err = execute("append", p, "--end")
	require.NoError(t, err)
	assert.Contains(t, out, "ended the file")

	// With the End block in place, a waiting follow returns on its own.
	out, err = execute("follow", p, "--interval", "1ms")
	require.NoError(t, err)
	lines = followLines(t, out)
	require.Len(t, lines, 5)
	assert.Equal(t, "from stdin", payloadOf(t, lines[3]))
	assert.Equal(t, "End", lines[4].TypeName)

	out, err = execute("info", p, "--json")
	require.NoError(t, err)
	var info infoJSON
	require.NoError(t, json.Unmarshal([]byte(out), &info))
	require.NotNil(t, info.Appendable)
	assert.True(t, info.Appendable.Ended)
	require.NotNil(t, info.FileLength, "append --end back-patches file_length")

	_, err = execute("validate", p)
	require.NoError(t, err)

	_, err = execute("append", p, "--type", "2", "--data", "too late")
	assert.ErrorIs(t, err, binpazer.ErrEnded)
}

func TestFollowWaitsForTheWriter(t *testing.T) {
	p := packAppendable(t, true)

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := execute("follow", p, "--interval", "2ms")
		done <- result{out, err}
	}()

	// The writer is a second handle on the file, as a second process would be.
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	require.NoError(t, err)
	defer f.Close()
	bw, _, err := binpazer.ResumeAppendWriter(f)
	require.NoError(t, err)
	for _, rec := range []string{"live one", "live two"} {
		time.Sleep(10 * time.Millisecond)
		require.NoError(t, bw.Block(2, 0, []byte(rec)))
	}
	select {
	case r := <-done:
		t.Fatalf("follow returned before the End block: %q, %v", r.out, r.err)
	case <-time.After(30 * time.Millisecond):
	}
	require.NoError(t, bw.End())

	select {
	case r := <-done:
		require.NoError(t, r.err)
		lines := followLines(t, r.out)
		require.Len(t, lines, 5)
		assert.Equal(t, "live one", payloadOf(t, lines[2]))
		assert.Equal(t, "live two", payloadOf(t, lines[3]))
	case <-time.After(10 * time.Second):
		t.Fatal("follow did not return after the End block")
	}
}

func TestReadCommandsStopAtBytesInProgress(t *testing.T) {
	p := packAppendable(t, true)
	whole := sealedBlock(2, []byte("half written record"))
	appendBytes(t, p, whole[:len(whole)-5]) // the writer is part of the way through a block

	out, err := execute("follow", p, "--no-wait")
	require.NoError(t, err)
	assert.Len(t, followLines(t, out), 2, "a block still being written is not printed")

	out, err = execute("info", p)
	require.NoError(t, err)
	assert.Contains(t, out, "appendable:  open (")
	assert.Contains(t, out, "bytes in progress at offset")

	out, err = execute("dump", p, "--json")
	require.NoError(t, err)
	var d dumpJSON
	require.NoError(t, json.Unmarshal([]byte(out), &d))
	assert.Len(t, d.Blocks, 2)

	out, err = execute("find", p, "--type", "2", "--json")
	require.NoError(t, err)
	var fr findResult
	require.NoError(t, json.Unmarshal([]byte(out), &fr))
	assert.Len(t, fr.Matches, 2, "find lists committed blocks only")

	data, err := os.ReadFile(p)
	require.NoError(t, err)
	res, err := validateJSON(t, data)
	require.NoError(t, err, "bytes in progress are not a validation failure")
	require.NotNil(t, res.InProgress)
	assert.EqualValues(t, len(whole)-5, res.InProgress.Bytes)

	// The rest of the block lands: the same commands now see it.
	appendBytes(t, p, whole[len(whole)-5:])
	out, err = execute("follow", p, "--no-wait")
	require.NoError(t, err)
	assert.Len(t, followLines(t, out), 3)
}

func TestAppendSealsATornTail(t *testing.T) {
	p := packAppendable(t, true)
	torn := sealedBlock(2, []byte("an interrupted writer left this"))
	torn[10] ^= 0xFF // all the bytes are there, but the seal no longer verifies
	appendBytes(t, p, torn)

	_, err := execute("follow", p, "--no-wait")
	require.ErrorIs(t, err, binpazer.ErrTorn)
	_, err = execute("info", p)
	require.ErrorIs(t, err, binpazer.ErrTorn, "a torn block is reported, not read around")

	data, err := os.ReadFile(p)
	require.NoError(t, err)
	res, verr := validateJSON(t, data)
	assertFails(t, res, verr, ruleCRC)

	out, err := execute("append", p)
	require.NoError(t, err)
	assert.Contains(t, out, "sealed a torn tail")

	out, err = execute("follow", p, "--no-wait", "--no-payload")
	require.NoError(t, err)
	lines := followLines(t, out)
	require.Len(t, lines, 3)
	assert.Equal(t, "Padding", lines[2].TypeName)
	_, err = execute("validate", p)
	require.NoError(t, err)

	out, err = execute("append", p)
	require.NoError(t, err)
	assert.NotContains(t, out, "sealed", "a clean file needs no repair")
}

func TestFollowWarnsOnceAboutATornBlockAndRecovers(t *testing.T) {
	p := packAppendable(t, true)
	torn := sealedBlock(2, []byte("torn"))
	torn[9] ^= 0xFF
	appendBytes(t, p, torn)

	type result struct {
		out, errOut string
		err         error
	}
	done := make(chan result, 1)
	go func() {
		out, errOut, err := executeIO(nil, "follow", p, "--interval", "2ms")
		done <- result{out, errOut, err}
	}()
	time.Sleep(30 * time.Millisecond)

	f, err := os.OpenFile(p, os.O_RDWR, 0)
	require.NoError(t, err)
	defer f.Close()
	bw, _, err := binpazer.ResumeAppendWriter(f)
	require.NoError(t, err)
	require.NoError(t, bw.End())

	select {
	case r := <-done:
		require.NoError(t, r.err)
		assert.Equal(t, 1, strings.Count(r.errOut, "warning:"), "stderr: %q", r.errOut)
		assert.Len(t, followLines(t, r.out), 4, "two records, the padding, the End block")
	case <-time.After(10 * time.Second):
		t.Fatal("follow did not return after the file was resumed and ended")
	}
}

func TestFollowAStream(t *testing.T) {
	data, err := os.ReadFile(packAppendable(t, false))
	require.NoError(t, err)

	// io.MultiReader hides Seek, so the command takes its pipe path.
	out, _, err := executeIO(io.MultiReader(bytes.NewReader(data)), "follow", "-")
	require.NoError(t, err)
	assert.Len(t, followLines(t, out), 3)

	open, err := os.ReadFile(packAppendable(t, true))
	require.NoError(t, err)
	_, _, err = executeIO(io.MultiReader(bytes.NewReader(open)), "follow", "-")
	require.ErrorContains(t, err, "before the End block")
}

func TestFollowAndAppendRefusePlainFiles(t *testing.T) {
	p, _ := packSample(t, t.TempDir())
	_, err := execute("follow", p, "--no-wait")
	assert.ErrorIs(t, err, binpazer.ErrNotAppendable)
	_, err = execute("append", p, "--type", "1", "--data", "x")
	assert.ErrorIs(t, err, binpazer.ErrNotAppendable)
	_, err = execute("follow", p, "--interval", "0s")
	assert.Error(t, err)
}

func TestAppendArgumentErrors(t *testing.T) {
	p := packAppendable(t, true)
	before, err := os.ReadFile(p)
	require.NoError(t, err)

	_, err = execute("append", p, "--type", "7", "--data", "x")
	assert.ErrorContains(t, err, "type 7 is not in the file's block type table")
	_, err = execute("append", p, "--data", "x")
	assert.ErrorContains(t, err, "--type is required")
	_, err = execute("append", p, "--type", "2", "--data", "x", "--data-base64", "eA==")
	assert.ErrorContains(t, err, "at most one")
	_, err = execute("append", p, "--type", "2", "--flags", "nonsense")
	assert.ErrorContains(t, err, "unknown flag")
	_, err = execute("append", p, "--type", "2", "--codec", "nonsense")
	assert.ErrorContains(t, err, "unknown codec")
	_, err = execute("append", filepath.Join(t.TempDir(), "missing.binpazer"), "--end")
	assert.Error(t, err)

	after, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a refused append writes nothing")

	_, err = execute("append", p, "--type", "2", "--data-base64", "aGVsbG8=")
	require.NoError(t, err)
	out, err := execute("follow", p, "--no-wait")
	require.NoError(t, err)
	lines := followLines(t, out)
	assert.Equal(t, "hello", payloadOf(t, lines[len(lines)-1]))
}

func TestAppendableUnpackPackRoundTrip(t *testing.T) {
	for _, open := range []bool{true, false} {
		p := packAppendable(t, open)
		want, err := os.ReadFile(p)
		require.NoError(t, err)

		dir := t.TempDir()
		_, err = execute("unpack", p, "-d", dir)
		require.NoError(t, err)
		raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		require.NoError(t, err)
		var m Manifest
		require.NoError(t, json.Unmarshal(raw, &m))
		assert.True(t, m.Appendable)
		assert.Equal(t, open, m.Open)
		assert.Len(t, m.Blocks, 2, "the End block is not a manifest block")
		assert.Len(t, m.Types, 1, "the End type is not a manifest type")

		again := filepath.Join(dir, "again.binpazer")
		_, err = execute("pack", filepath.Join(dir, "manifest.json"), "-o", again)
		require.NoError(t, err)
		got, err := os.ReadFile(again)
		require.NoError(t, err)
		assert.Equal(t, want, got, "open=%v", open)
	}
}

func TestPackAppendableRefusals(t *testing.T) {
	dir := t.TempDir()
	write := func(m Manifest) string {
		m.WriterGUID = "aabbccdd-0000-0000-0000-000000000001"
		raw, err := json.Marshal(m)
		require.NoError(t, err)
		p := filepath.Join(dir, "m.json")
		require.NoError(t, os.WriteFile(p, raw, 0o644))
		return p
	}
	_, err := execute("pack", write(Manifest{Open: true}), "-o", filepath.Join(dir, "a.binpazer"))
	assert.ErrorContains(t, err, `"open" needs "appendable"`)
	_, err = execute("pack", write(Manifest{Appendable: true, Index: true}), "-o", filepath.Join(dir, "b.binpazer"))
	assert.ErrorContains(t, err, "no Block Index")
}
