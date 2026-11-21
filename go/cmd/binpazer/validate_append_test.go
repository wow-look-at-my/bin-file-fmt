package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// appendableArchive builds a conformant appendable file with multiple sealed
// records. A seekable End back-patches file_length; a stream End leaves the sentinel.
func appendableArchive(t *testing.T, end, seekable bool) []byte {
	t.Helper()
	g, err := binpazer.ParseGUID("deadbeef-0000-0000-0000-000000000000")
	require.NoError(t, err)
	wg, err := binpazer.ParseGUID("aabbccdd-0000-0000-0000-000000000001")
	require.NoError(t, err)
	mf := &memFile{}
	var buf bytes.Buffer
	var w io.Writer = &buf
	if seekable {
		w = mf
	}
	bw, err := binpazer.NewAppendWriter(w, wg, "demo", []binpazer.TypeDef{{TypeID: 2, GUID: g, Name: "Note"}})
	require.NoError(t, err)
	require.NoError(t, bw.Block(2, 0, []byte("first record")))
	require.NoError(t, bw.Block(2, 0, []byte("second")))
	if end {
		require.NoError(t, bw.End())
	}
	if seekable {
		return append([]byte(nil), mf.buf...)
	}
	return buf.Bytes()
}

// blockOffsets returns the offset of every top-level block header in data.
func blockOffsets(t *testing.T, data []byte) []uint64 {
	t.Helper()
	br, err := binpazer.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	var offs []uint64
	for {
		b, err := br.Next()
		if err == io.EOF {
			return offs
		}
		require.NoError(t, err)
		offs = append(offs, b.Offset())
		require.NoError(t, br.SkipPayload(b))
	}
}

func validateStreamJSON(t *testing.T, data []byte) (*validateResult, error) {
	t.Helper()
	out, _, err := executeIO(nonSeekable{bytes.NewReader(data)}, "validate", "-", "--json")
	var res validateResult
	require.NoError(t, json.Unmarshal([]byte(out), &res), "JSON output: %q", out)
	return &res, err
}

func TestValidateAppendableGoodFiles(t *testing.T) {
	for name, data := range map[string][]byte{
		"open":         appendableArchive(t, false, true),
		"ended":        appendableArchive(t, true, true),
		"ended stream": appendableArchive(t, true, false),
	} {
		res, err := validateJSON(t, data)
		require.NoError(t, err, name)
		assert.Empty(t, res.Findings, name)
		assert.Nil(t, res.InProgress, name)
		for _, r := range res.Rules {
			assert.Equal(t, "pass", r.Status, "%s: rule %s", name, r.Rule)
		}
	}
}

func TestValidateAppendableInProgressTail(t *testing.T) {
	data := appendableArchive(t, false, true)
	whole := sealedBlock(2, []byte("still being written"))

	res, err := validateJSON(t, append(bytes.Clone(data), whole[:11]...))
	require.NoError(t, err)
	require.NotNil(t, res.InProgress)
	assert.EqualValues(t, len(data), res.InProgress.Offset)
	assert.EqualValues(t, 11, res.InProgress.Bytes)

	// A pipe cannot tell how many bytes follow, but the unfinished tail is still not a failure.
	res, err = validateStreamJSON(t, append(bytes.Clone(data), whole[:11]...))
	require.NoError(t, err)
	require.NotNil(t, res.InProgress)
	assert.EqualValues(t, len(data), res.InProgress.Offset)

	// The same cut in an ended file is damage: nothing is in progress after the End block is promised.
	ended := appendableArchive(t, true, true)
	res, err = validateJSON(t, ended[:len(ended)-3])
	require.Error(t, err)
	assert.Nil(t, res.InProgress)

	out, err := execute("validate", writeTemp(t, append(bytes.Clone(data), whole[:11]...)))
	require.NoError(t, err)
	assert.Contains(t, out, "in progress: the writer has not ended the file")
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.binpazer")
	require.NoError(t, os.WriteFile(p, data, 0o644))
	return p
}

func TestValidateRuleSeal(t *testing.T) {
	data := appendableArchive(t, true, true)
	offs := blockOffsets(t, data)
	flags := binary.LittleEndian.Uint16(data[offs[0]+2:])
	binary.LittleEndian.PutUint16(data[offs[0]+2:], flags&^binpazer.FlagHasCRC)

	res, err := validateJSON(t, data)
	assertFails(t, res, err, ruleSeal)
	res, err = validateStreamJSON(t, data)
	assertFails(t, res, err, ruleSeal)
}

func TestValidateRuleAppendIndex(t *testing.T) {
	t.Run("index block", func(t *testing.T) {
		data := appendableArchive(t, false, true)
		// A sealed, well-formed Block Index with no entries.
		data = append(data, sealedBlock(binpazer.TypeBlockIndex, make([]byte, 8))...)
		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleAppendIndex)
	})
	t.Run("footer", func(t *testing.T) {
		data := appendableArchive(t, true, true)
		offs := blockOffsets(t, data)
		footer := binary.LittleEndian.AppendUint64(nil, offs[0])
		data = append(data, append(footer, "rezapnib"...)...)
		binary.LittleEndian.PutUint64(data[32:], uint64(len(data)))
		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleAppendIndex)
	})
}

func TestValidateRuleEnd(t *testing.T) {
	t.Run("in a file that is not appendable", func(t *testing.T) {
		_, data := writeArchive(t, t.TempDir(), "plain.binpazer", func(bw *binpazer.Writer) {
			require.NoError(t, bw.Block(binpazer.TypeEnd, binpazer.FlagHasCRC, nil))
		})
		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleEnd)
		assert.Empty(t, findRule(res, ruleUnknownTypeID), "the End type is predefined")
	})
	t.Run("payload not empty", func(t *testing.T) {
		data := append(appendableArchive(t, false, true), sealedBlock(binpazer.TypeEnd, []byte("x"))...)
		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleEnd)
	})
	t.Run("bytes after it, sentinel length", func(t *testing.T) {
		data := append(appendableArchive(t, true, false), sealedBlock(2, []byte("late"))...)
		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleEnd)
	})
	t.Run("bytes after it, known length", func(t *testing.T) {
		data := append(appendableArchive(t, true, true), sealedBlock(2, []byte("late"))...)
		binary.LittleEndian.PutUint64(data[32:], uint64(len(data)))
		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleEnd)
	})
	t.Run("a stream cannot look past it", func(t *testing.T) {
		res, err := validateStreamJSON(t, appendableArchive(t, true, false))
		require.NoError(t, err)
		st := ruleStatusOf(t, res, ruleEnd)
		assert.Equal(t, "skipped", st.Status)
		assert.Contains(t, st.Reason, "seekable")
	})
}

func TestValidateAppendableTornBlockFailsCRC(t *testing.T) {
	data := appendableArchive(t, false, true)
	offs := blockOffsets(t, data)
	data[offs[1]+9] ^= 0xFF
	res, err := validateJSON(t, data)
	assertFails(t, res, err, ruleCRC)
	assert.Nil(t, res.InProgress, "a torn block is a failure, not bytes in progress")
}

func TestValidateHeaderFlagBits(t *testing.T) {
	data := appendableArchive(t, true, true)
	data[48] |= 0x02 // a reserved header flag bit
	res, err := validateJSON(t, data)
	assertFails(t, res, err, ruleReservedZero)
}
