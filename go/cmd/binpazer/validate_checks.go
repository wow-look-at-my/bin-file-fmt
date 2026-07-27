package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// Rule ids, one per item of SPEC.md's "Validation" checklist plus the checks
// the checklist implies (CRC trailers, footer/index consistency, general
// parseability).
const (
	ruleMagic          = "magic"                 // magic and signature match exactly
	ruleFirstBlockBTT  = "first_block_btt"       // the first block is type_id 65534
	ruleBTTDupID       = "btt_dup_type_id"       // no duplicate type_id in the BTT
	ruleBTTInvalidID   = "btt_invalid_type_id"   // the BTT never contains 0 or 65535
	ruleBlockInvalidID = "block_invalid_type_id" // block headers never use 0 or 65535
	ruleUnknownTypeID  = "unknown_type_id"       // every block's type_id is in the table (predefined ids exempt)
	ruleReservedZero   = "reserved_nonzero"      // reserved fields and flag bits are 0
	ruleAlignment      = "alignment"             // blocks start 8-byte aligned
	ruleLength         = "length_mismatch"       // file_length matches the input; the last block ends at it
	ruleStringTerm     = "string_term"           // a string's byte_length bytes are followed by 0x00
	ruleStringUTF8     = "string_utf8"           // string data is valid UTF-8
	ruleCRC            = "crc"                   // stored CRC-32C trailers match
	ruleIndex          = "index"                 // the footer and Block Index point at real blocks of the claimed types
	ruleEnvelope       = "compression_envelope"  // a compressed block carries a whole, well-formed Compression Envelope
	ruleStructure      = "structure"             // the input parses: no truncation, capacity, or version problems
)

// allRules lists every rule in report order.
var allRules = []string{
	ruleMagic, ruleFirstBlockBTT, ruleBTTDupID, ruleBTTInvalidID,
	ruleBlockInvalidID, ruleUnknownTypeID, ruleReservedZero, ruleAlignment,
	ruleLength, ruleStringTerm, ruleStringUTF8, ruleCRC, ruleIndex, ruleEnvelope,
	ruleStructure,
}

// knownFlagsMask covers the five defined block flag bits (bit 2 is
// `compressed`, defined in 1.1); the rest are reserved and must be 0.
const knownFlagsMask = binpazer.FlagCritical | binpazer.FlagSafeToCopy |
	binpazer.FlagCompressed | binpazer.FlagHasCRC | binpazer.FlagLargeMode

// finding is one concrete rule violation.
type finding struct {
	Rule   string `json:"rule"`
	Offset uint64 `json:"offset"`
	Detail string `json:"detail"`
}

// ruleStatus is the per-check verdict: pass, fail, or skipped (with a reason).
type ruleStatus struct {
	Rule   string `json:"rule"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// validateResult is the machine-readable shape of a validate run.
type validateResult struct {
	OK       bool         `json:"ok"`
	Findings []finding    `json:"findings"`
	Rules    []ruleStatus `json:"rules"`
}

// validator accumulates findings and skip reasons across the passes.
type validator struct {
	findings []finding
	skipped  map[string]string
}

func newValidator() *validator {
	return &validator{skipped: map[string]string{}}
}

func (v *validator) add(rule string, off uint64, format string, args ...any) {
	v.findings = append(v.findings, finding{Rule: rule, Offset: off, Detail: fmt.Sprintf(format, args...)})
}

func (v *validator) skip(rule, reason string) { v.skipped[rule] = reason }

// finish materializes the per-rule statuses: fail when the rule has a
// finding, else the recorded skip, else pass.
func (v *validator) finish() *validateResult {
	failed := map[string]bool{}
	for _, f := range v.findings {
		failed[f.Rule] = true
	}
	res := &validateResult{Findings: v.findings, OK: len(v.findings) == 0}
	if res.Findings == nil {
		res.Findings = []finding{}
	}
	for _, r := range allRules {
		st := ruleStatus{Rule: r, Status: "pass"}
		if failed[r] {
			st.Status = "fail"
		} else if reason, ok := v.skipped[r]; ok {
			st.Status = "skipped"
			st.Reason = reason
		}
		res.Rules = append(res.Rules, st)
	}
	return res
}

// fatalInit records a NewReader failure under the right rule and marks every
// other rule unevaluated.
func (v *validator) fatalInit(err error) {
	rule := ruleStructure
	switch {
	case errors.Is(err, binpazer.ErrBadMagic):
		rule = ruleMagic
	case strings.Contains(err.Error(), "first block is not the block type table"):
		rule = ruleFirstBlockBTT
	}
	v.add(rule, 0, "%v", err)
	for _, r := range allRules {
		if r != rule {
			v.skip(r, "not evaluated: the input failed to parse")
		}
	}
}

// bttChecks validates the Block Type Table's ids: no duplicates, never 0 or
// 65535. Works on streams (the table is already parsed).
func (v *validator) bttChecks(br *binpazer.Reader) {
	seen := map[uint16]int{}
	for i, t := range br.Types {
		if t.TypeID == 0 || t.TypeID == binpazer.TypeInvalidMax {
			v.add(ruleBTTInvalidID, br.FirstBlockOffset, "type table entry %d uses invalid type_id %d", i, t.TypeID)
		}
		if j, dup := seen[t.TypeID]; dup {
			v.add(ruleBTTDupID, br.FirstBlockOffset, "type_id %d defined by both entry %d and entry %d", t.TypeID, j, i)
		} else {
			seen[t.TypeID] = i
		}
	}
}

// utf8Checks validates that the parsed strings are UTF-8 (SPEC.md "String":
// data is UTF-8). Works on streams.
func (v *validator) utf8Checks(br *binpazer.Reader) {
	if !utf8.ValidString(br.WriterName) {
		v.add(ruleStringUTF8, 60, "writer_name is not valid UTF-8")
	}
	for i, t := range br.Types {
		if !utf8.ValidString(t.Name) {
			v.add(ruleStringUTF8, br.FirstBlockOffset, "type table entry %d name is not valid UTF-8", i)
		}
	}
	if br.FirstBlockOffset%8 != 0 {
		v.add(ruleAlignment, 40, "first_block_offset %d is not 8-byte aligned", br.FirstBlockOffset)
	}
}

// walk checks every block: known type id, valid id, reserved flag bits,
// alignment, and the CRC trailer (streamed, never buffered). It reports
// whether an index block was seen and whether the walk reached a clean EOF.
func (v *validator) walk(br *binpazer.Reader) (indexBlockSeen, clean bool) {
	n := 0
	for {
		b, err := br.Next()
		if err == io.EOF {
			return indexBlockSeen, true
		}
		if err != nil {
			v.add(ruleStructure, br.Offset(), "block %d: %v", n, err)
			return indexBlockSeen, false
		}
		off := b.Offset()
		if b.TypeID == binpazer.TypeInvalid || b.TypeID == binpazer.TypeInvalidMax {
			v.add(ruleBlockInvalidID, off, "block %d uses invalid type_id %d", n, b.TypeID)
		} else if !b.Known && !isPredefined(b.TypeID) {
			v.add(ruleUnknownTypeID, off, "block %d: type_id %d not in the block type table", n, b.TypeID)
		}
		if rb := b.Flags &^ knownFlagsMask; rb != 0 {
			v.add(ruleReservedZero, off, "block %d sets reserved flag bits %#04x", n, rb)
		}
		if off%8 != 0 {
			v.add(ruleAlignment, off, "block %d starts at unaligned offset %d", n, off)
		}
		if b.TypeID == binpazer.TypeBlockIndex {
			indexBlockSeen = true
		}
		// A compressed block must carry a whole, well-formed envelope. Only the
		// envelope HEADER is inspected: validation is about the file's
		// structure, and decoding would need a codec this build may not have.
		// The header rides along with the payload stream, so a compressed
		// block costs one pass whether or not it also carries a CRC.
		var env *headCapture
		if b.Flags&binpazer.FlagCompressed != 0 {
			env = &headCapture{}
		}
		// Stream CRC-carrying payloads through the checksum in 64 KiB
		// chunks; skip the rest without reading. Nothing is buffered.
		if b.Flags&binpazer.FlagHasCRC != 0 || env != nil {
			var sink io.Writer = io.Discard
			if env != nil {
				sink = env
			}
			_, err = br.WritePayloadTo(b, sink)
			if err != nil && errors.Is(err, binpazer.ErrCRCMismatch) {
				v.add(ruleCRC, off, "block %d (type %d): %v", n, b.TypeID, err)
				err = br.SkipPayload(b) // realign past the pad and keep walking
			}
		} else {
			err = br.SkipPayload(b)
		}
		if env != nil {
			v.envelopeChecks(b, n, env.head[:env.n])
		}
		if err != nil {
			v.add(ruleStructure, br.Offset(), "block %d (type %d): %v", n, b.TypeID, err)
			return indexBlockSeen, false
		}
		n++
	}
}

// headCapture keeps the first bytes written through it and discards the rest,
// so a payload can be streamed (for the CRC) while its envelope header is
// captured, with nothing buffered.
type headCapture struct {
	head [32]byte
	n    int
}

func (h *headCapture) Write(p []byte) (int, error) {
	if h.n < len(h.head) {
		h.n += copy(h.head[h.n:], p)
	}
	return len(p), nil
}

// envelopeChecks validates a compressed block's Compression Envelope header
// against SPEC.md: a whole header must be present, the codec id must not be 0,
// and the envelope's reserved fields must be 0. An unassigned codec id is a
// file from a later revision, not a violation -- and is deliberately not
// reported as one.
func (v *validator) envelopeChecks(b *binpazer.Block, n int, head []byte) {
	off := b.Offset()
	if len(head) < 16 {
		v.add(ruleEnvelope, off, "block %d is compressed but its payload holds %d bytes; the envelope header needs 16", n, len(head))
		return
	}
	codec := binary.LittleEndian.Uint16(head[0:])
	if codec == binpazer.CodecInvalid {
		v.add(ruleEnvelope, off, "block %d: compression envelope codec id is 0 (invalid)", n)
	}
	if cf := binary.LittleEndian.Uint16(head[2:]); cf != 0 {
		v.add(ruleEnvelope, off, "block %d: compression envelope codec_flags = %#x (must be 0)", n, cf)
	}
	if rv := binary.LittleEndian.Uint32(head[4:]); rv != 0 {
		v.add(ruleEnvelope, off, "block %d: compression envelope reserved field = %#x (must be 0)", n, rv)
	}
	if codec == binpazer.CodecByGUID && (b.PayloadLen < 32 || len(head) < 32) {
		v.add(ruleEnvelope, off, "block %d: GUID-identified codec needs a 32-byte envelope header, payload holds %d bytes", n, b.PayloadLen)
	}
}

// lengthChecks compares file_length against the real input size (seekable
// only) and, after a clean walk, against where the blocks actually ended.
// The streaming sentinel exempts both (SPEC.md).
func (v *validator) lengthChecks(br *binpazer.Reader, clean bool) {
	if br.FileLength == binpazer.FileLengthUnknown {
		return
	}
	if size := br.InputSize(); size >= 0 {
		if int64(br.FileLength) != size {
			v.add(ruleLength, 32, "file_length=%d but the input is %d bytes", br.FileLength, size)
			return
		}
	} else {
		v.skip(ruleLength, "file_length vs input size requires seekable input; end-of-blocks position still checked")
	}
	if !clean {
		return // the walk already failed; the end position is meaningless
	}
	// Seekable readers stop right before a footer (file_length-16); a stream
	// discovers and consumes the footer, landing exactly at file_length.
	end := br.Offset()
	want := br.FileLength
	if br.HasIndex() && end == want-16 {
		return
	}
	if end != want {
		v.add(ruleLength, end, "blocks end at offset %d but file_length is %d", end, want)
	}
}
