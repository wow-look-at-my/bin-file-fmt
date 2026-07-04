package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInfoJSON(t *testing.T) {
	dir := t.TempDir()
	path, data := packSample(t, dir)

	out, _, err := executeIO(nil, "info", path, "--json")
	require.NoError(t, err)
	var res infoJSON
	require.NoError(t, json.Unmarshal([]byte(out), &res))

	assert.EqualValues(t, 1, res.VersionMajor)
	assert.Equal(t, "demo", res.WriterName)
	assert.Equal(t, "aabbccdd-0000-0000-0000-000000000001", res.WriterGUID)
	require.NotNil(t, res.FileLength)
	assert.True(t, res.HasIndex)
	assert.Len(t, res.Types, 3, "BlockTypeTable + Mesh + Note")
	require.Len(t, res.Blocks, 4, "3 data blocks + the index block")
	assert.Equal(t, "Mesh", res.Blocks[0].TypeName)
	assert.Equal(t, []string{"critical", "has_crc"}, res.Blocks[0].Flags)
	assert.EqualValues(t, 8, res.Blocks[0].PayloadLen)
	assert.Equal(t, "BlockIndex", res.Blocks[3].TypeName)

	// On a pipe, the footer is discovered during the walk; has_index must
	// still come out true because the JSON is assembled afterwards.
	out, _, err = executeIO(nonSeekable{bytes.NewReader(data)}, "info", "-", "--json")
	require.NoError(t, err)
	var streamed infoJSON
	require.NoError(t, json.Unmarshal([]byte(out), &streamed))
	assert.True(t, streamed.HasIndex)
	assert.Equal(t, res.Blocks, streamed.Blocks, "block listing identical on streams")
}

func TestDumpJSON(t *testing.T) {
	dir := t.TempDir()
	path, _ := packSample(t, dir)

	out, _, err := executeIO(nil, "dump", path, "--json")
	require.NoError(t, err)
	var res dumpJSON
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Len(t, res.Blocks, 4)
	payload, err := base64.StdEncoding.DecodeString(res.Blocks[0].PayloadB64)
	require.NoError(t, err)
	assert.Equal(t, "vertices", string(payload))

	// --block selects a single entry; the object shape stays the same.
	out, _, err = executeIO(nil, "dump", path, "--json", "--block", "1")
	require.NoError(t, err)
	res = dumpJSON{}
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Len(t, res.Blocks, 1)
	assert.Equal(t, 1, res.Blocks[0].Index)
	payload, err = base64.StdEncoding.DecodeString(res.Blocks[0].PayloadB64)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(payload))

	// Out-of-range --block fails in JSON mode too.
	_, _, err = executeIO(nil, "dump", path, "--json", "--block", "99")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}

func TestJSONRecurse(t *testing.T) {
	dir := t.TempDir()
	path := groupArchive(t, dir)

	out, _, err := executeIO(nil, "info", path, "--json", "--recurse")
	require.NoError(t, err)
	var res infoJSON
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Len(t, res.Blocks, 3)

	g := res.Blocks[0] // group with a CRC leaf + a nested group
	require.Len(t, g.Children, 2)
	assert.Equal(t, "Mesh", g.Children[0].TypeName)
	require.NotNil(t, g.Children[0].CRCOK)
	assert.True(t, *g.Children[0].CRCOK)
	require.Len(t, g.Children[1].Children, 1, "nested group descended")
	assert.Equal(t, "Note", g.Children[1].Children[0].TypeName)
	assert.Empty(t, g.Children[0].PayloadB64, "info embeds no payloads")

	assert.Len(t, res.Blocks[1].Children, 1, "GUID-mapped group type descended")
	assert.NotEmpty(t, res.Blocks[2].Error, "malformed group reported on the subtree")

	// dump --json --recurse embeds leaf payloads.
	out, _, err = executeIO(nil, "dump", path, "--json", "--recurse")
	require.NoError(t, err)
	var dres dumpJSON
	require.NoError(t, json.Unmarshal([]byte(out), &dres))
	require.Len(t, dres.Blocks, 3)
	require.Len(t, dres.Blocks[0].Children, 2)
	payload, err := base64.StdEncoding.DecodeString(dres.Blocks[0].Children[0].PayloadB64)
	require.NoError(t, err)
	assert.Equal(t, "mesh-data", string(payload))
	deep, err := base64.StdEncoding.DecodeString(dres.Blocks[0].Children[1].Children[0].PayloadB64)
	require.NoError(t, err)
	assert.Equal(t, "deep", string(deep))
	assert.Empty(t, dres.Blocks[0].PayloadB64, "a descended group has children, not an opaque payload")
}
