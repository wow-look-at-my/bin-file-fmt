package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStdinReadCommands runs info/validate/dump against "-" with both a
// seekable stdin (redirected file semantics) and a non-seekable one (pipe
// semantics), comparing against the file-path output.
func TestStdinReadCommands(t *testing.T) {
	dir := t.TempDir()
	path, data := packSample(t, dir)

	for _, cmd := range []string{"info", "validate", "dump"} {
		want, err := execute(cmd, path)
		require.NoError(t, err, cmd)

		// Seekable stdin: byte-identical output, full features (index probe).
		got, _, err := executeIO(bytes.NewReader(data), cmd, "-")
		require.NoError(t, err, cmd)
		assert.Equal(t, want, got, "%s -: seekable stdin output differs", cmd)

		// Non-seekable stdin: same result, except info's has_index (the
		// footer is only discovered when the walk reaches it).
		got, _, err = executeIO(nonSeekable{bytes.NewReader(data)}, cmd, "-")
		require.NoError(t, err, cmd)
		if cmd == "info" {
			assert.Contains(t, got, "has_index:   false")
			assert.Equal(t,
				strings.Replace(want, "has_index:   true", "has_index:   false", 1), got,
				"info -: non-seekable output should differ only in has_index")
		} else {
			assert.Equal(t, want, got, "%s -: non-seekable stdin output differs", cmd)
		}
	}
}

// TestStdinPipeOSFile covers the *os.File probe: a real pipe end must be
// detected as non-seekable and still parse.
func TestStdinPipeOSFile(t *testing.T) {
	dir := t.TempDir()
	_, data := packSample(t, dir)

	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	go func() {
		_, _ = pw.Write(data)
		pw.Close()
	}()
	defer pr.Close()

	out, _, err := executeIO(pr, "info", "-")
	require.NoError(t, err)
	assert.Contains(t, out, "writer:      demo")
	assert.Contains(t, out, "has_index:   false")
}

func TestPackToStdoutByteIdentical(t *testing.T) {
	dir := t.TempDir()
	path, want := packSample(t, dir)
	mp := filepath.Join(dir, "manifest.json")

	stdout, stderr, err := executeIO(nil, "pack", mp, "-o", "-")
	require.NoError(t, err)
	assert.Equal(t, string(want), stdout, "pack -o - bytes differ from pack -o %s", path)
	assert.Contains(t, stderr, "wrote")
	assert.NotContains(t, stdout, "wrote", "status message must not pollute the archive stream")
}

func TestPackManifestFromStdin(t *testing.T) {
	dir := t.TempDir()
	_, want := packSample(t, dir)

	// Rewrite the manifest without file references (stdin manifests resolve
	// files against the cwd) -- inline the big block as base64 instead.
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	require.NoError(t, err)
	var m Manifest
	require.NoError(t, json.Unmarshal(raw, &m))
	big, err := os.ReadFile(filepath.Join(dir, "big.bin"))
	require.NoError(t, err)
	for i := range m.Blocks {
		if m.Blocks[i].File != "" {
			m.Blocks[i].File = ""
			m.Blocks[i].DataBase64 = base64.StdEncoding.EncodeToString(big)
		}
	}
	inlined, err := json.Marshal(&m)
	require.NoError(t, err)

	stdout, _, err := executeIO(bytes.NewReader(inlined), "pack", "-", "-o", "-")
	require.NoError(t, err)
	assert.Equal(t, string(want), stdout, "pack from stdin manifest differs from file-based pack")
}

// TestPipePackToInfo emulates `binpazer pack ... -o - | binpazer info -`.
func TestPipePackToInfo(t *testing.T) {
	dir := t.TempDir()
	mp := writeSampleManifest(t, dir)

	archive, _, err := executeIO(nil, "pack", mp, "-o", "-")
	require.NoError(t, err)

	out, _, err := executeIO(nonSeekable{strings.NewReader(archive)}, "info", "-")
	require.NoError(t, err)
	assert.Contains(t, out, "writer:      demo")
	assert.Contains(t, out, "Mesh")

	// And the validate leg of the pipe.
	out, _, err = executeIO(nonSeekable{strings.NewReader(archive)}, "validate", "-")
	require.NoError(t, err)
	assert.Contains(t, out, "spec-conformant")
}

func TestUnpackFromStdinRoundTrip(t *testing.T) {
	dir := t.TempDir()
	_, data := packSample(t, dir)

	udir := filepath.Join(dir, "unpacked")
	require.NoError(t, os.MkdirAll(udir, 0o755))
	out, _, err := executeIO(nonSeekable{bytes.NewReader(data)}, "unpack", "-", "-d", udir)
	require.NoError(t, err)
	assert.Contains(t, out, "unpacked")

	// The stream-discovered footer must survive into the manifest so a
	// repack reproduces the archive byte for byte.
	raw, err := os.ReadFile(filepath.Join(udir, "manifest.json"))
	require.NoError(t, err)
	var m Manifest
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.True(t, m.Index, "index flag lost when unpacking from a pipe")

	repacked, _, err := executeIO(nil, "pack", filepath.Join(udir, "manifest.json"), "-o", "-")
	require.NoError(t, err)
	assert.Equal(t, string(data), repacked, "unpack - | pack round-trip not byte-identical")
}

// TestMaxAllocCapsStreams proves --max-alloc bounds claims on non-seekable
// input: a 4 KiB block must be rejected under a 512-byte cap.
func TestMaxAllocCapsStreams(t *testing.T) {
	dir := t.TempDir()
	_, data := packSample(t, dir)

	_, _, err := executeIO(nonSeekable{bytes.NewReader(data)}, "validate", "-", "--max-alloc", "512")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cap")
}
