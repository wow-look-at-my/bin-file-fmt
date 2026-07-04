package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractByType(t *testing.T) {
	dir := t.TempDir()
	path, data := packSample(t, dir)
	big, err := os.ReadFile(filepath.Join(dir, "big.bin"))
	require.NoError(t, err)

	// Default --index 0, CRC-carrying block, via the Block Index.
	stdout, _, err := executeIO(nil, "extract", path, "--type", "1")
	require.NoError(t, err)
	assert.Equal(t, "vertices", stdout)

	// Second block of the same type.
	stdout, _, err = executeIO(nil, "extract", path, "--type", "1", "--index", "1")
	require.NoError(t, err)
	assert.Equal(t, string(big), stdout)

	// Alias + --out file.
	outFile := filepath.Join(dir, "note.bin")
	_, _, err = executeIO(nil, "cat", path, "--type", "2", "--out", outFile)
	require.NoError(t, err)
	got, err := os.ReadFile(outFile)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))

	// Non-seekable stdin: sequential fallback, same bytes.
	stdout, _, err = executeIO(nonSeekable{bytes.NewReader(data)}, "extract", "-", "--type", "1", "--index", "1")
	require.NoError(t, err)
	assert.Equal(t, string(big), stdout)
}

func TestExtractNoIndexFallsBackToWalk(t *testing.T) {
	dir := t.TempDir()
	mp := writeSampleManifest(t, dir)

	// Re-pack the sample without a Block Index so Find must walk.
	raw, err := os.ReadFile(mp)
	require.NoError(t, err)
	var m Manifest
	require.NoError(t, json.Unmarshal(raw, &m))
	m.Index = false
	noIdx, err := json.Marshal(&m)
	require.NoError(t, err)
	mp2 := filepath.Join(dir, "noindex.json")
	require.NoError(t, os.WriteFile(mp2, noIdx, 0o644))
	out := filepath.Join(dir, "noindex.binpazer")
	_, err = execute("pack", mp2, "-o", out)
	require.NoError(t, err)

	stdout, _, err := executeIO(nil, "extract", out, "--type", "2")
	require.NoError(t, err)
	assert.Equal(t, "hello", stdout)
}

func TestExtractErrors(t *testing.T) {
	dir := t.TempDir()
	path, data := packSample(t, dir)

	_, err := execute("extract", path, "--type", "7")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no blocks of type 7")

	_, err = execute("extract", path, "--type", "1", "--index", "5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")

	_, err = execute("extract", path, "--type", "1", "--index", "-1")
	require.Error(t, err)

	// A failed extract to a file must not leave a partial file behind.
	outFile := filepath.Join(dir, "partial.bin")
	_, _, err = executeIO(nil, "extract", path, "--type", "7", "--out", outFile)
	require.Error(t, err)
	assert.NoFileExists(t, outFile)

	// Same range errors on the stream path.
	_, _, err = executeIO(nonSeekable{bytes.NewReader(data)}, "extract", "-", "--type", "7")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no blocks of type 7")
}

func TestFindListsMatches(t *testing.T) {
	dir := t.TempDir()
	path, data := packSample(t, dir)

	out, err := execute("find", path, "--type", "1")
	require.NoError(t, err)
	assert.Contains(t, out, "[0] offset=")
	assert.Contains(t, out, "[1] offset=")

	// JSON shape, and index/walk agreement: the seekable run resolves
	// through the Block Index, the stream run walks -- offsets must match.
	jsonOut, _, err := executeIO(nil, "find", path, "--type", "1", "--json")
	require.NoError(t, err)
	var viaIndex findResult
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &viaIndex))
	require.Len(t, viaIndex.Matches, 2)
	assert.Equal(t, uint16(1), viaIndex.TypeID)
	assert.Equal(t, []string{"critical", "has_crc"}, viaIndex.Matches[0].Flags)
	assert.Equal(t, uint64(8), viaIndex.Matches[0].PayloadLen)

	jsonOut, _, err = executeIO(nonSeekable{bytes.NewReader(data)}, "find", "-", "--type", "1", "--json")
	require.NoError(t, err)
	var viaWalk findResult
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &viaWalk))
	assert.Equal(t, viaIndex, viaWalk, "index-based and walk-based find disagree")

	// No matches: JSON still emitted, but the exit is nonzero.
	jsonOut, _, err = executeIO(nil, "find", path, "--type", "9", "--json")
	require.Error(t, err)
	var empty findResult
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &empty))
	assert.Empty(t, empty.Matches)

	_, err = execute("find", path, "--type", "9")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no blocks of type 9")
}
