// bench_test.go tests the hidden bench command against its output contract.

package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// fnvBlocks is an independent FNV-1a over the bench sample bytes of each payload.
func fnvBlocks(payloads ...[]byte) string {
	h := uint32(2166136261)
	mix := func(b byte) { h = (h ^ uint32(b)) * 16777619 }
	for _, p := range payloads {
		var l [4]byte
		binary.LittleEndian.PutUint32(l[:], uint32(len(p)))
		for _, b := range l {
			mix(b)
		}
		if len(p) > 0 {
			mix(p[0])
			mix(p[len(p)/2])
			mix(p[len(p)-1])
		}
	}
	return fmt.Sprintf("%08x", h)
}

func benchLine(op string, blocks, n int, sum string) string {
	line, err := json.Marshal(struct {
		Op       string `json:"op"`
		Blocks   int    `json:"blocks"`
		Bytes    int    `json:"bytes"`
		Checksum string `json:"checksum"`
	}{op, blocks, n, sum})
	if err != nil {
		panic(err)
	}
	return string(line) + "\n"
}

func repeat(p []byte, n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = p
	}
	return out
}

// benchFile writes payload to dir and runs bench write with it, returning the output path.
func benchFile(t *testing.T, dir, name string, payload []byte, blocks int, codec string) string {
	t.Helper()
	pp := filepath.Join(dir, name+".payload")
	require.NoError(t, os.WriteFile(pp, payload, 0o644))
	out := filepath.Join(dir, name+".binpazer")
	o, err := execute("bench", "write", out, pp, fmt.Sprint(blocks), codec)
	require.NoError(t, err)
	assert.Equal(t, benchLine("write", blocks, blocks*len(payload), "00000000"), o)
	return out
}

func samplePayload(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte(i*7 + i/13)
	}
	return p
}

func TestBenchWriteReadEveryCodec(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		codec   string
		payload []byte
	}{
		{"none", samplePayload(1000)},
		{"none", nil},
		{"none", []byte{1, 2, 3}},
		{"stored", samplePayload(1000)},
		{"deflate", samplePayload(1000)},
		{"zstd", samplePayload(1000)},
		{"zstd", nil},
		{"zstd", []byte{1, 2, 3}},
		{"lz4", samplePayload(1000)},
	}
	for i, c := range cases {
		name := fmt.Sprintf("%s-%d", c.codec, i)
		t.Run(name, func(t *testing.T) {
			f := benchFile(t, dir, name, c.payload, 5, c.codec)
			o, err := execute("bench", "read", f)
			require.NoError(t, err)
			assert.Equal(t, benchLine("read", 5, 5*len(c.payload), fnvBlocks(repeat(c.payload, 5)...)), o)
		})
	}
}

func TestBenchLineIsExact(t *testing.T) {
	dir := t.TempDir()
	f := benchFile(t, dir, "lit", []byte{1, 2, 3}, 1, "none")
	o, err := execute("bench", "read", f)
	require.NoError(t, err)
	const head, tail = `{"op":"read","blocks":1,"bytes":3,"checksum":"`, "\"}\n"
	require.True(t, strings.HasPrefix(o, head), o)
	require.True(t, strings.HasSuffix(o, tail), o)
	assert.Equal(t, fnvBlocks([]byte{1, 2, 3}), strings.TrimSuffix(strings.TrimPrefix(o, head), tail))
}

func TestBenchStreamAndFind(t *testing.T) {
	dir := t.TempDir()
	p := samplePayload(70000)
	f := benchFile(t, dir, "s", p, 4, "none")

	o, err := execute("bench", "stream", f)
	require.NoError(t, err)
	assert.Equal(t, benchLine("stream", 4, 4*len(p), fnvBlocks(repeat(p, 4)...)), o)

	o, err = execute("bench", "find", f, "9")
	require.NoError(t, err)
	assert.Equal(t, benchLine("find", 9, 9*len(p), fnvBlocks(repeat(p, 9)...)), o)

	e := benchFile(t, dir, "e", nil, 2, "none")
	o, err = execute("bench", "stream", e)
	require.NoError(t, err)
	assert.Equal(t, benchLine("stream", 2, 0, fnvBlocks(nil, nil)), o)
}

func TestBenchStreamCompressedUsesStoredBytes(t *testing.T) {
	dir := t.TempDir()
	f := benchFile(t, dir, "z", samplePayload(5000), 1, "zstd")
	br, fh, err := openBench(f)
	require.NoError(t, err)
	defer fh.Close()
	b, err := br.Next()
	require.NoError(t, err)
	stored, err := br.ReadPayload(b)
	require.NoError(t, err)

	o, err := execute("bench", "stream", f)
	require.NoError(t, err)
	assert.Equal(t, benchLine("stream", 1, len(stored), fnvBlocks(stored)), o)
}

func TestBenchWriteMatchesLibrary(t *testing.T) {
	dir := t.TempDir()
	p := samplePayload(333)
	f := benchFile(t, dir, "lib", p, 3, "none")
	got, err := os.ReadFile(f)
	require.NoError(t, err)

	lf, err := os.Create(filepath.Join(dir, "direct.binpazer"))
	require.NoError(t, err)
	defer lf.Close()
	bw, err := binpazer.NewWriter(lf, binpazer.GUID{0xB0}, "bench",
		[]binpazer.TypeDef{{TypeID: 1, GUID: binpazer.GUID{0x11}, Name: "Payload"}})
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		require.NoError(t, bw.Block(1, binpazer.FlagHasCRC, p))
	}
	require.NoError(t, bw.WriteIndex())
	require.NoError(t, bw.End())
	want, err := os.ReadFile(lf.Name())
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestBenchErrors(t *testing.T) {
	dir := t.TempDir()
	good := benchFile(t, dir, "good", samplePayload(100), 2, "none")
	raw, err := os.ReadFile(good)
	require.NoError(t, err)
	br, fh, err := openBench(good)
	require.NoError(t, err)
	offs, err := br.Find(1)
	require.NoError(t, err)
	fh.Close()

	corrupt := filepath.Join(dir, "corrupt.binpazer")
	bad := bytes.Clone(raw)
	bad[offs[0]+8+50] ^= 0xFF
	require.NoError(t, os.WriteFile(corrupt, bad, 0o644))

	truncated := filepath.Join(dir, "trunc.binpazer")
	require.NoError(t, os.WriteFile(truncated, raw[:offs[0]+4], 0o644))

	garbage := filepath.Join(dir, "garbage.binpazer")
	require.NoError(t, os.WriteFile(garbage, []byte("not a binpazer file at all, really"), 0o644))

	empty := benchFile(t, dir, "zero", samplePayload(10), 0, "none")
	missing := filepath.Join(dir, "missing")
	pp := filepath.Join(dir, "good.payload")

	cases := [][]string{
		{"read", missing},
		{"read", garbage},
		{"read", corrupt},
		{"read", truncated},
		{"stream", corrupt},
		{"find", corrupt, "3"},
		{"find", missing, "3"},
		{"find", empty, "3"},
		{"find", good, "x"},
		{"write", filepath.Join(dir, "o"), missing, "2", "none"},
		{"write", filepath.Join(dir, "o"), pp, "2", "bogus"},
		{"write", filepath.Join(dir, "o"), pp, "two", "none"},
		{"write", filepath.Join(dir, "o"), pp, "-1", "none"},
		{"write", filepath.Join(missing, "o"), pp, "2", "none"},
	}
	for _, c := range cases {
		t.Run(fmt.Sprint(c), func(t *testing.T) {
			stdout, _, err := executeIO(nil, append([]string{"bench"}, c...)...)
			require.Error(t, err)
			assert.Empty(t, stdout)
		})
	}
}

func TestBenchCPUProfile(t *testing.T) {
	dir := t.TempDir()
	f := benchFile(t, dir, "p", samplePayload(100), 2, "none")
	prof := filepath.Join(dir, "cpu.prof")
	t.Cleanup(func() { _, _ = execute("bench", "--cpuprofile", "", "read", f) })

	o, err := execute("bench", "--cpuprofile", prof, "read", f)
	require.NoError(t, err)
	assert.Equal(t, benchLine("read", 2, 200, fnvBlocks(repeat(samplePayload(100), 2)...)), o)
	st, err := os.Stat(prof)
	require.NoError(t, err)
	assert.Positive(t, st.Size())

	stdout, _, err := executeIO(nil, "bench", "--cpuprofile", filepath.Join(dir, "no", "cpu.prof"), "read", f)
	require.Error(t, err)
	assert.Empty(t, stdout)

	var held bytes.Buffer
	require.NoError(t, pprof.StartCPUProfile(&held))
	stdout, _, err = executeIO(nil, "bench", "--cpuprofile", filepath.Join(dir, "busy.prof"), "read", f)
	pprof.StopCPUProfile()
	require.Error(t, err)
	assert.Empty(t, stdout)
}
