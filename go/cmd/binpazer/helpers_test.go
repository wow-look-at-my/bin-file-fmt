package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

// resetFlags restores every command flag to its default so a value set by one
// execute call cannot leak.
func resetFlags() {
	for _, c := range rootCmd.Commands() {
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Changed {
				_ = f.Value.Set(f.DefValue)
				f.Changed = false
			}
		})
	}
}

// execute runs the CLI with args, capturing combined output.
func execute(args ...string) (string, error) {
	var buf bytes.Buffer
	resetFlags()
	rootCmd.SetIn(nil)
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return buf.String(), err
}

// executeIO runs the CLI with the given stdin, capturing stdout and stderr
// separately (so tests can make byte-exact assertions on stdout).
func executeIO(in io.Reader, args ...string) (stdout, stderr string, err error) {
	var ob, eb bytes.Buffer
	resetFlags()
	rootCmd.SetIn(in)
	rootCmd.SetOut(&ob)
	rootCmd.SetErr(&eb)
	rootCmd.SetArgs(args)
	err = rootCmd.Execute()
	rootCmd.SetIn(nil)
	return ob.String(), eb.String(), err
}

// writeSampleManifest writes a small pack manifest (user types, blocks,
// index enabled) plus its referenced payload file into dir and returns
// the manifest path.
func writeSampleManifest(t *testing.T, dir string) string {
	t.Helper()
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
	p := filepath.Join(dir, "manifest.json")
	require.NoError(t, os.WriteFile(p, raw, 0o644))
	return p
}

// packSample packs the sample manifest into dir and returns the archive path
// and its bytes.
func packSample(t *testing.T, dir string) (string, []byte) {
	t.Helper()
	mp := writeSampleManifest(t, dir)
	out := filepath.Join(dir, "sample.binpazer")
	_, err := execute("pack", mp, "-o", out)
	require.NoError(t, err)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	return out, data
}
