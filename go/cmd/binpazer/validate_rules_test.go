package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// validateJSON runs `validate --json` on data (written to a temp file) and
// returns the parsed result plus the command error (nil means conformant).
func validateJSON(t *testing.T, data []byte) (*validateResult, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.binpazer")
	require.NoError(t, os.WriteFile(p, data, 0o644))
	out, _, err := executeIO(nil, "validate", p, "--json")
	var res validateResult
	require.NoError(t, json.Unmarshal([]byte(out), &res), "JSON output: %q", out)
	return &res, err
}

func findRule(res *validateResult, rule string) []finding {
	var out []finding
	for _, f := range res.Findings {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

func ruleStatusOf(t *testing.T, res *validateResult, rule string) ruleStatus {
	t.Helper()
	for _, r := range res.Rules {
		if r.Rule == rule {
			return r
		}
	}
	t.Fatalf("rule %s missing from result", rule)
	return ruleStatus{}
}

// assertFails asserts the run failed with at least one finding of rule.
func assertFails(t *testing.T, res *validateResult, err error, rule string) {
	t.Helper()
	require.Error(t, err)
	assert.False(t, res.OK)
	assert.NotEmpty(t, findRule(res, rule), "expected a %s finding, got %+v", rule, res.Findings)
	assert.Equal(t, "fail", ruleStatusOf(t, res, rule).Status)
}

// validArchive builds a conformant two-block archive (block 0 carries a CRC).
func validArchive(t *testing.T, withIndex bool) []byte {
	t.Helper()
	_, data := writeArchive(t, t.TempDir(), "valid.binpazer", func(bw *binpazer.Writer) {
		require.NoError(t, bw.Block(1, binpazer.FlagHasCRC, []byte("mesh-data")))
		require.NoError(t, bw.Block(2, 0, []byte("note")))
		if withIndex {
			require.NoError(t, bw.WriteIndex())
		}
	})
	// Guard the fixed offsets the corruption tests patch ("demo" writer,
	// first block at 72, 4 BTT entries starting at 88).
	require.EqualValues(t, 72, binary.LittleEndian.Uint64(data[40:]), "first_block_offset drifted")
	require.EqualValues(t, 0, data[64], "writer_name NUL drifted")
	require.EqualValues(t, binpazer.TypeBlockTypeTable, binary.LittleEndian.Uint16(data[72:]))
	require.EqualValues(t, 4, binary.LittleEndian.Uint32(data[80:]), "BTT entry count drifted")
	require.EqualValues(t, 14, binary.LittleEndian.Uint32(data[108:]), "entry 0 name length drifted")
	require.EqualValues(t, 0, data[126], "entry 0 name NUL drifted")
	return data
}

// indexOffsetOf locates the Block Index via the footer.
func indexOffsetOf(t *testing.T, data []byte) uint64 {
	t.Helper()
	br, err := binpazer.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	off, ok := br.IndexOffset()
	require.True(t, ok, "fixture must carry an index")
	return off
}

func TestValidateGoodFileAllRulesPass(t *testing.T) {
	res, err := validateJSON(t, validArchive(t, true))
	require.NoError(t, err)
	assert.True(t, res.OK)
	assert.Empty(t, res.Findings)
	require.Len(t, res.Rules, len(allRules))
	for _, r := range res.Rules {
		assert.Equal(t, "pass", r.Status, "rule %s", r.Rule)
	}
}

func TestValidateRuleMagic(t *testing.T) {
	data := validArchive(t, false)
	data[0] ^= 0xFF
	res, err := validateJSON(t, data)
	assertFails(t, res, err, ruleMagic)

	data = validArchive(t, false)
	data[8] = 0x00 // signature corruption (the CRLF guard)
	res, err = validateJSON(t, data)
	assertFails(t, res, err, ruleMagic)
	assert.Equal(t, "skipped", ruleStatusOf(t, res, ruleCRC).Status, "later rules are unevaluated on fatal parse errors")
}

func TestValidateRuleFirstBlockBTT(t *testing.T) {
	data := validArchive(t, false)
	binary.LittleEndian.PutUint16(data[72:], 1) // first block claims a user type
	res, err := validateJSON(t, data)
	assertFails(t, res, err, ruleFirstBlockBTT)
}

func TestValidateRuleBTTIDs(t *testing.T) {
	g1, err := binpazer.ParseGUID("11223344-5566-7788-99aa-bbccddeeff00")
	require.NoError(t, err)
	g2, err := binpazer.ParseGUID("deadbeef-0000-0000-0000-000000000000")
	require.NoError(t, err)
	mf := &memFile{}
	bw, err := binpazer.NewWriter(mf, g1, "demo", []binpazer.TypeDef{
		{TypeID: 1, GUID: g1, Name: "A"},
		{TypeID: 1, GUID: g2, Name: "B"}, // duplicate id
		{TypeID: 0, GUID: g2, Name: "Z"}, // invalid id
	})
	require.NoError(t, err)
	require.NoError(t, bw.Block(1, 0, []byte("x")))
	require.NoError(t, bw.End())

	res, err := validateJSON(t, mf.buf)
	assertFails(t, res, err, ruleBTTDupID)
	assert.NotEmpty(t, findRule(res, ruleBTTInvalidID))
}

func TestValidateRuleBlockIDs(t *testing.T) {
	_, data := writeArchive(t, t.TempDir(), "ids.binpazer", func(bw *binpazer.Writer) {
		require.NoError(t, bw.Block(0, 0, []byte("zero")))        // invalid
		require.NoError(t, bw.Block(65535, 0, []byte("max")))     // invalid
		require.NoError(t, bw.Block(42, 0, []byte("unknown")))    // not in the table
		require.NoError(t, bw.Block(65531, 0, []byte("pad\x00"))) // predefined: exempt
	})
	res, err := validateJSON(t, data)
	assertFails(t, res, err, ruleBlockInvalidID)
	assert.Len(t, findRule(res, ruleBlockInvalidID), 2)
	require.Len(t, findRule(res, ruleUnknownTypeID), 1, "invalid and predefined ids must not double-report as unknown")
	assert.Contains(t, findRule(res, ruleUnknownTypeID)[0].Detail, "type_id 42")
}

func TestValidateRuleReservedZero(t *testing.T) {
	patches := map[string]int{
		"header_flags":    48,
		"header_reserved": 52,
		"btt_reserved":    84,
		"entry_flags":     90,
	}
	for name, off := range patches {
		t.Run(name, func(t *testing.T) {
			data := validArchive(t, false)
			data[off] |= 0x01
			res, err := validateJSON(t, data)
			assertFails(t, res, err, ruleReservedZero)
		})
	}

	t.Run("block_flag_bits", func(t *testing.T) {
		_, data := writeArchive(t, t.TempDir(), "flags.binpazer", func(bw *binpazer.Writer) {
			require.NoError(t, bw.Block(1, 1<<7, []byte("x"))) // reserved bit 7
		})
		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleReservedZero)
		assert.Contains(t, findRule(res, ruleReservedZero)[0].Detail, "reserved flag bits")
	})

	// Bit 5 is the lowest STILL-reserved flag bit (bit 2 became `compressed` in
	// 1.1). Patch it into the has_crc block of an otherwise valid file,
	// re-sealing the CRC the way a writer that set the bit would have (the
	// CRC-32C covers the 8-byte header): the ONLY finding must be the reserved
	// bit, not a CRC mismatch.
	t.Run("reserved_flag_bit", func(t *testing.T) {
		data := validArchive(t, false)
		br, err := binpazer.NewReader(bytes.NewReader(data))
		require.NoError(t, err)
		b, err := br.Next()
		require.NoError(t, err)
		require.EqualValues(t, 1, b.TypeID, "first walked block must be the has_crc mesh block")
		require.NotZero(t, b.Flags&binpazer.FlagHasCRC)
		off := b.Offset()
		data[off+2] |= 1 << 5
		end := off + 8 + b.PayloadLen
		crc := crc32.Checksum(data[off:end], crc32.MakeTable(crc32.Castagnoli))
		binary.LittleEndian.PutUint32(data[end:], crc)

		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleReservedZero)
		require.Len(t, res.Findings, 1, "the reserved bit must be the only finding: %+v", res.Findings)
		assert.Contains(t, res.Findings[0].Detail, "reserved flag bits 0x0020")
	})

	t.Run("index_reserved", func(t *testing.T) {
		data := validArchive(t, true)
		idx := indexOffsetOf(t, data)
		data[idx+12] |= 0x01 // index payload reserved u32
		data[idx+18] |= 0x01 // entry 0 reserved u16
		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleReservedZero)
		assert.Len(t, findRule(res, ruleReservedZero), 2)
	})
}

func TestValidateRuleAlignment(t *testing.T) {
	data := validArchive(t, true)
	idx := indexOffsetOf(t, data)
	// Entry 0's target offset: make it unaligned.
	target := binary.LittleEndian.Uint64(data[idx+24:])
	binary.LittleEndian.PutUint64(data[idx+24:], target+4)
	res, err := validateJSON(t, data)
	assertFails(t, res, err, ruleAlignment)
}

func TestValidateRuleLength(t *testing.T) {
	t.Run("trailing_junk", func(t *testing.T) {
		data := append(validArchive(t, false), make([]byte, 8)...)
		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleLength)
		assert.Contains(t, findRule(res, ruleLength)[0].Detail, "file_length")
	})

	t.Run("stream_ends_early", func(t *testing.T) {
		// Cut the stream at the second block's boundary: a pipe cannot check
		// the size up front, but the walk's clean-yet-early EOF must fire
		// the end-of-blocks position check.
		data := validArchive(t, false)
		br, err := binpazer.NewReader(bytes.NewReader(data))
		require.NoError(t, err)
		b0, err := br.Next()
		require.NoError(t, err)
		require.NoError(t, br.SkipPayload(b0))
		b1, err := br.Next()
		require.NoError(t, err)
		truncated := data[:b1.Offset()]

		out, _, err := executeIO(nonSeekable{bytes.NewReader(truncated)}, "validate", "-", "--json")
		require.Error(t, err)
		var res validateResult
		require.NoError(t, json.Unmarshal([]byte(out), &res))
		assert.NotEmpty(t, findRule(&res, ruleLength))
		assert.Contains(t, findRule(&res, ruleLength)[0].Detail, "blocks end at offset")
	})
}

func TestValidateRuleStringTerm(t *testing.T) {
	data := validArchive(t, false)
	data[64] = '!'  // writer_name terminator
	data[126] = '!' // BTT entry 0 name terminator
	res, err := validateJSON(t, data)
	assertFails(t, res, err, ruleStringTerm)
	assert.Len(t, findRule(res, ruleStringTerm), 2)
}

func TestValidateRuleStringUTF8(t *testing.T) {
	g1, err := binpazer.ParseGUID("11223344-5566-7788-99aa-bbccddeeff00")
	require.NoError(t, err)
	mf := &memFile{}
	bw, err := binpazer.NewWriter(mf, g1, "de\xffmo", nil) // invalid UTF-8 writer name
	require.NoError(t, err)
	require.NoError(t, bw.End())
	res, err := validateJSON(t, mf.buf)
	assertFails(t, res, err, ruleStringUTF8)
}

func TestValidateRuleCRC(t *testing.T) {
	data := validArchive(t, false)
	i := bytes.Index(data, []byte("mesh-data"))
	require.Positive(t, i)
	data[i] ^= 0xFF
	res, err := validateJSON(t, data)
	assertFails(t, res, err, ruleCRC)
	assert.Equal(t, "pass", ruleStatusOf(t, res, ruleStructure).Status,
		"the walk must continue past a CRC mismatch")
	assert.Equal(t, "pass", ruleStatusOf(t, res, ruleLength).Status)
}

// compressedArchive builds a conformant file whose second block is compressed
// (codec `stored`, so no codec dependency), for the envelope corruption tests.
func compressedArchive(t *testing.T) []byte {
	t.Helper()
	_, data := writeArchive(t, t.TempDir(), "compressed.binpazer", func(bw *binpazer.Writer) {
		require.NoError(t, bw.Block(1, binpazer.FlagHasCRC, []byte("mesh-data")))
		require.NoError(t, bw.CompressedBlock(2, 0, binpazer.CodecStored, []byte("compressed note")))
	})
	return data
}

// envelopeOffsetOf returns the file offset of the compressed block's envelope
// header (i.e. the start of its payload).
func envelopeOffsetOf(t *testing.T, data []byte) uint64 {
	t.Helper()
	br, err := binpazer.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	for {
		b, err := br.Next()
		require.NoError(t, err)
		if b.Flags&binpazer.FlagCompressed != 0 {
			return b.Offset() + 8
		}
		require.NoError(t, br.SkipPayload(b))
	}
}

func TestValidateRuleCompressionEnvelope(t *testing.T) {
	// A conformant compressed file passes every rule, including the new one.
	t.Run("conformant", func(t *testing.T) {
		res, err := validateJSON(t, compressedArchive(t))
		require.NoError(t, err)
		assert.True(t, res.OK, "findings: %+v", res.Findings)
		assert.Equal(t, "pass", ruleStatusOf(t, res, ruleEnvelope).Status)
		assert.Equal(t, "pass", ruleStatusOf(t, res, ruleReservedZero).Status,
			"bit 2 is `compressed` in 1.1, not a reserved bit")
	})

	t.Run("codec_id_zero", func(t *testing.T) {
		data := compressedArchive(t)
		env := envelopeOffsetOf(t, data)
		data[env], data[env+1] = 0, 0
		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleEnvelope)
		assert.Contains(t, findRule(res, ruleEnvelope)[0].Detail, "codec id is 0")
	})

	t.Run("codec_flags_reserved", func(t *testing.T) {
		data := compressedArchive(t)
		env := envelopeOffsetOf(t, data)
		data[env+2] |= 1
		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleEnvelope)
		assert.Contains(t, findRule(res, ruleEnvelope)[0].Detail, "codec_flags")
	})

	t.Run("envelope_reserved_u32", func(t *testing.T) {
		data := compressedArchive(t)
		env := envelopeOffsetOf(t, data)
		data[env+4] |= 1
		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleEnvelope)
		assert.Contains(t, findRule(res, ruleEnvelope)[0].Detail, "reserved field")
	})

	// A compressed flag on a payload too short to hold an envelope header:
	// the file walks, and the envelope rule is what catches it.
	t.Run("payload_too_short_for_header", func(t *testing.T) {
		data := validArchive(t, false)
		br, err := binpazer.NewReader(bytes.NewReader(data))
		require.NoError(t, err)
		b, err := br.Next()
		require.NoError(t, err)
		off := b.Offset()
		data[off+2] |= byte(binpazer.FlagCompressed)
		end := off + 8 + b.PayloadLen
		crc := crc32.Checksum(data[off:end], crc32.MakeTable(crc32.Castagnoli))
		binary.LittleEndian.PutUint32(data[end:], crc)

		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleEnvelope)
		assert.Contains(t, findRule(res, ruleEnvelope)[0].Detail, "envelope header needs 16")
	})
}

func TestValidateRuleIndex(t *testing.T) {
	t.Run("footer_points_at_wrong_block", func(t *testing.T) {
		data := validArchive(t, true)
		binary.LittleEndian.PutUint64(data[len(data)-16:], 72) // the BTT, type 65534
		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleIndex)
		assert.Contains(t, findRule(res, ruleIndex)[0].Detail, "not the Block Index")
	})

	t.Run("entry_claims_wrong_type", func(t *testing.T) {
		data := validArchive(t, true)
		idx := indexOffsetOf(t, data)
		binary.LittleEndian.PutUint16(data[idx+16:], 42) // entry 0: type 1 -> 42
		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleIndex)
		assert.Contains(t, findRule(res, ruleIndex)[0].Detail, "claims type 42")
	})

	t.Run("index_without_footer", func(t *testing.T) {
		data := validArchive(t, true)
		data[len(data)-1] ^= 0xFF // break the footer magic
		res, err := validateJSON(t, data)
		assertFails(t, res, err, ruleIndex)
		assert.Contains(t, findRule(res, ruleIndex)[0].Detail, "no footer locates it")
	})
}

func TestValidateRuleStructure(t *testing.T) {
	data := validArchive(t, false)
	res, err := validateJSON(t, data[:len(data)-4]) // cut into the final block
	require.Error(t, err)
	assert.False(t, res.OK)
	// Either the walk or the size check must catch it; the walk's finding is
	// the structural one.
	assert.NotEmpty(t, res.Findings)
}

func TestValidateStreamSkips(t *testing.T) {
	data := validArchive(t, true)
	out, _, err := executeIO(nonSeekable{bytes.NewReader(data)}, "validate", "-", "--json")
	require.NoError(t, err, "skips must not fail a conformant stream")
	var res validateResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.True(t, res.OK)
	for _, rule := range []string{ruleStringTerm, ruleIndex, ruleReservedZero} {
		st := ruleStatusOf(t, &res, rule)
		assert.Equal(t, "skipped", st.Status, rule)
		assert.Contains(t, st.Reason, "seekable", rule)
	}
	assert.Equal(t, "pass", ruleStatusOf(t, &res, ruleCRC).Status)
}
