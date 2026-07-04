package main

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// childBytes builds one on-disk nested block: header + payload (+ CRC when
// has_crc) + zero pad to 8 bytes.
func childBytes(tid, flags uint16, payload []byte) []byte {
	var h [8]byte
	binary.LittleEndian.PutUint16(h[0:], tid)
	binary.LittleEndian.PutUint16(h[2:], flags)
	binary.LittleEndian.PutUint32(h[4:], uint32(len(payload)))
	b := append(h[:], payload...)
	if flags&binpazer.FlagHasCRC != 0 {
		tab := crc32.MakeTable(crc32.Castagnoli)
		crc := crc32.Update(0, tab, h[:])
		crc = crc32.Update(crc, tab, payload)
		var cb [4]byte
		binary.LittleEndian.PutUint32(cb[:], crc)
		b = append(b, cb[:]...)
	}
	for len(b)%8 != 0 {
		b = append(b, 0)
	}
	return b
}

// writeArchive writes blocks into a fresh binpazer file (two user types plus
// type 9 mapped to the Group GUID) and returns its path and bytes.
func writeArchive(t *testing.T, dir, name string, add func(*binpazer.Writer)) (string, []byte) {
	t.Helper()
	g1, err := binpazer.ParseGUID("11223344-5566-7788-99aa-bbccddeeff00")
	require.NoError(t, err)
	g2, err := binpazer.ParseGUID("deadbeef-0000-0000-0000-000000000000")
	require.NoError(t, err)
	wg, err := binpazer.ParseGUID("aabbccdd-0000-0000-0000-000000000001")
	require.NoError(t, err)
	mf := &memFile{}
	bw, err := binpazer.NewWriter(mf, wg, "demo", []binpazer.TypeDef{
		{TypeID: 1, GUID: g1, Name: "Mesh"},
		{TypeID: 2, GUID: g2, Name: "Note"},
		{TypeID: 9, GUID: binpazer.GroupGUID, Name: "MyGroup"},
	})
	require.NoError(t, err)
	add(bw)
	require.NoError(t, bw.End())
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, mf.buf, 0o644))
	return p, append([]byte(nil), mf.buf...)
}

// groupArchive: block 0 = a group holding a CRC leaf and a nested group;
// block 1 = a group through the GUID mapping (type 9); block 2 = a malformed
// group whose payload is not a block sequence.
func groupArchive(t *testing.T, dir string) string {
	t.Helper()
	inner := childBytes(1, binpazer.FlagHasCRC, []byte("mesh-data"))
	nested := childBytes(binpazer.TypeGroup, 0, childBytes(2, 0, []byte("deep")))
	p, _ := writeArchive(t, dir, "groups.binpazer", func(bw *binpazer.Writer) {
		require.NoError(t, bw.Block(binpazer.TypeGroup, 0, append(append([]byte{}, inner...), nested...)))
		require.NoError(t, bw.Block(9, 0, childBytes(2, 0, []byte("via-guid"))))
		require.NoError(t, bw.Block(binpazer.TypeGroup, 0, []byte("junk!")))
	})
	return p
}

func TestInfoRecurse(t *testing.T) {
	dir := t.TempDir()
	path := groupArchive(t, dir)

	// Without --recurse the groups stay opaque.
	out, err := execute("info", path)
	require.NoError(t, err)
	assert.NotContains(t, out, "[0.0]")

	out, err = execute("info", path, "--recurse")
	require.NoError(t, err)
	assert.Contains(t, out, "[0.0]", "group children listed")
	assert.Contains(t, out, "Mesh")
	assert.Contains(t, out, "[0.1.0]", "nested group descended")
	assert.Contains(t, out, "[1.0]", "GUID-mapped group type descended")
	assert.Contains(t, out, "error in group 2", "malformed group reported inline")
}

func TestDumpRecurse(t *testing.T) {
	dir := t.TempDir()
	path := groupArchive(t, dir)

	out, err := execute("dump", path, "--recurse")
	require.NoError(t, err)
	assert.Contains(t, out, "block 0 (type 65530, ")
	assert.Contains(t, out, "group")
	assert.Contains(t, out, "block 0.0 (type 1, 9 bytes):")
	assert.Contains(t, out, "mesh-data", "leaf payload hex-dumped")
	assert.Contains(t, out, "block 0.1.0 (type 2, 4 bytes):")
	assert.Contains(t, out, "deep")
	assert.Contains(t, out, "error in group 2")

	// Without --recurse the group payload is one opaque hex blob.
	out, err = execute("dump", path, "--block", "0")
	require.NoError(t, err)
	assert.NotContains(t, out, "block 0.0")
}

func TestRecurseDepthLimit(t *testing.T) {
	dir := t.TempDir()
	deep := childBytes(2, 0, []byte("bottom"))
	for i := 0; i < 40; i++ {
		deep = childBytes(binpazer.TypeGroup, 0, deep)
	}
	// deep is now a child block; its payload is 40 levels of nesting.
	path, _ := writeArchive(t, dir, "deep.binpazer", func(bw *binpazer.Writer) {
		plen := int(binary.LittleEndian.Uint32(deep[4:]))
		require.NoError(t, bw.Block(binpazer.TypeGroup, 0, deep[8:8+plen]))
	})

	out, err := execute("info", path, "--recurse")
	require.NoError(t, err)
	assert.Contains(t, out, "nesting depth exceeds 32; not descending")
}

func TestRecurseGroupOwnCRCMismatch(t *testing.T) {
	dir := t.TempDir()
	inner := childBytes(1, 0, []byte("mesh-data"))
	path, data := writeArchive(t, dir, "crcgroup.binpazer", func(bw *binpazer.Writer) {
		require.NoError(t, bw.Block(binpazer.TypeGroup, binpazer.FlagHasCRC, inner))
		require.NoError(t, bw.Block(2, 0, []byte("after")))
	})
	i := bytes.Index(data, []byte("mesh-data"))
	require.Positive(t, i)
	data[i] ^= 0xFF
	require.NoError(t, os.WriteFile(path, data, 0o644))

	out, err := execute("info", path, "--recurse")
	require.NoError(t, err, "a group CRC failure is reported per subtree, not fatal")
	assert.Contains(t, out, "error in group 0")
	assert.Contains(t, out, "CRC mismatch")
	assert.Contains(t, out, "[1]", "walk continues past the damaged group")
}
