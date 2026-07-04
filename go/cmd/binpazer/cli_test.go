package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIEndToEnd(t *testing.T) {
	dir := t.TempDir()
	big := make([]byte, 4096)
	for i := range big {
		big[i] = byte(i)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "big.bin"), big, 0o644))

	m := &Manifest{
		WriterGUID: "aabbccdd-0000-0000-0000-000000000001",
		WriterName: "demo",
		Types: []ManifestType{
			{TypeID: 1, GUID: "11223344-5566-7788-99aa-bbccddeeff00", Name: "Mesh"},
			{TypeID: 2, GUID: "deadbeef-0000-0000-0000-000000000000", Name: "Note"},
		},
		Blocks: []ManifestBlock{
			{TypeID: 1, Flags: []string{"critical", "has_crc"}, Data: "vertices"},
			{TypeID: 2, DataBase64: base64.StdEncoding.EncodeToString([]byte("hello"))},
			{TypeID: 1, Flags: []string{"large_mode"}, File: "big.bin"},
		},
		Index: true,
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	require.NoError(t, err)
	manifestPath := filepath.Join(dir, "manifest.json")
	require.NoError(t, os.WriteFile(manifestPath, raw, 0o644))
	out := filepath.Join(dir, "out.binpazer")

	o, err := execute("pack", manifestPath, "-o", out)
	require.NoError(t, err)
	assert.Contains(t, o, "wrote")

	o, err = execute("validate", out)
	require.NoError(t, err)
	assert.Contains(t, o, "spec-conformant")

	o, err = execute("info", out)
	require.NoError(t, err)
	assert.Contains(t, o, "writer:      demo")
	assert.Contains(t, o, "Mesh")
	assert.Contains(t, o, "has_index:   true")

	o, err = execute("dump", out, "--block", "0")
	require.NoError(t, err)
	assert.Contains(t, o, "block 0")
	assert.Contains(t, o, "vertices")

	udir := filepath.Join(dir, "unpacked")
	o, err = execute("unpack", out, "-d", udir)
	require.NoError(t, err)
	assert.Contains(t, o, "unpacked")
	assert.FileExists(t, filepath.Join(udir, "manifest.json"))
	assert.FileExists(t, filepath.Join(udir, "block0.bin"))
	assert.FileExists(t, filepath.Join(udir, "block2.bin"))

	// re-pack the unpacked manifest -> still valid (round-trip)
	out2 := filepath.Join(dir, "out2.binpazer")
	_, err = execute("pack", filepath.Join(udir, "manifest.json"), "-o", out2)
	require.NoError(t, err)
	o, err = execute("validate", out2)
	require.NoError(t, err)
	assert.Contains(t, o, "spec-conformant")
}

func TestVersionFlag(t *testing.T) {
	out, err := execute("--version")
	require.NoError(t, err)
	assert.Contains(t, out, "binpazer version dev")
	assert.Contains(t, versionString(), "dev")
}

func TestDumpBlockOutOfRange(t *testing.T) {
	dir := t.TempDir()
	path, _ := packSample(t, dir)

	_, err := execute("dump", path, "--block", "99")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "block index 99 out of range")
	assert.Contains(t, err.Error(), "4 block(s)") // 3 data blocks + the index

	// The last valid index still works.
	out, err := execute("dump", path, "--block", "3")
	require.NoError(t, err)
	assert.Contains(t, out, "block 3")
}

func TestValidateRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.bin")
	require.NoError(t, os.WriteFile(bad, []byte("not a binpazer file at all"), 0o644))
	_, err := execute("validate", bad)
	require.Error(t, err)
}

func TestHelpers(t *testing.T) {
	// flag name <-> bits
	f, err := parseFlags([]string{"critical", "has_crc"})
	require.NoError(t, err)
	assert.Equal(t, []string{"critical", "has_crc"}, flagNames(f))

	_, err = parseFlags([]string{"bogus"})
	require.Error(t, err)

	// bad writer GUID surfaces an error from runPack
	require.Error(t, runPack(&Manifest{WriterGUID: "nope"}, "", &bytes.Buffer{}))

	// at most one content source per block
	_, err = ManifestBlock{Data: "x", File: "y"}.payload("")
	require.Error(t, err)

	assert.Equal(t, "Group", predefinedName(65530))
}
