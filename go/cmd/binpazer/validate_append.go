// validate_append.go holds the validate checks for appendable files.

package main

import (
	"errors"
	"io"

	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// nextBlock returns the next block of the walk, and io.EOF where the walk ends.
// In a seekable appendable file only committed blocks are walked.
// Bytes in progress end the walk and are noted; a torn block is a finding and comes back as ErrTorn.
func (v *validator) nextBlock(br *binpazer.Reader) (*binpazer.Block, error) {
	if !br.Appendable() || br.InputSize() < 0 {
		b, err := br.Next()
		if v.openTail(br, err, br.Offset()) {
			return nil, io.EOF
		}
		return b, err
	}
	b, err := br.NextCommitted()
	off, size := br.Offset(), uint64(br.InputSize())
	switch {
	case errors.Is(err, binpazer.ErrTorn):
		v.tornFinding(br, err)
		return nil, errWalkStopped
	case errors.Is(err, binpazer.ErrPending) && br.FileLength != binpazer.FileLengthUnknown:
		// A file with a known length is closed, so a block that is not all there is cut off.
		v.add(ruleStructure, off, "%v, but file_length says the file is complete", err)
		return nil, errWalkStopped
	case errors.Is(err, binpazer.ErrPending):
		if size > off {
			v.inProgress = &inProgress{Offset: off, Bytes: size - off}
		}
		return nil, io.EOF
	}
	return b, err
}

// errWalkStopped ends the walk after nextBlock has filed the finding that explains why.
var errWalkStopped = errors.New("walk stopped")

// tornFinding files a torn block under the rule it breaks: no seal at all, or a seal that does not verify.
func (v *validator) tornFinding(br *binpazer.Reader, torn error) {
	off := br.Offset()
	if b, err := br.Next(); err == nil && b.Flags&binpazer.FlagHasCRC == 0 {
		v.add(ruleSeal, off, "block at offset %d of an appendable file has no CRC seal", off)
		return
	}
	v.add(ruleCRC, off, "%v; the file needs a resume", torn)
}

// openTail reports whether err is the unfinished tail of an appendable stream that the writer has not ended.
// It notes the tail, which is not a finding.
func (v *validator) openTail(br *binpazer.Reader, err error, off uint64) bool {
	if !errors.Is(err, binpazer.ErrTruncated) || !br.Appendable() || br.Ended() ||
		br.FileLength != binpazer.FileLengthUnknown || br.InputSize() >= 0 {
		return false
	}
	v.inProgress = &inProgress{Offset: off}
	return true
}

// appendBlockChecks applies the appendable-file rules to one walked block.
func (v *validator) appendBlockChecks(br *binpazer.Reader, b *binpazer.Block, n int) {
	off := b.Offset()
	if b.TypeID == binpazer.TypeEnd {
		if !br.Appendable() {
			v.add(ruleEnd, off, "block %d is an End block, but the file is not appendable", n)
		}
		if b.PayloadLen != 0 {
			v.add(ruleEnd, off, "End block payload is %d bytes; it must be empty", b.PayloadLen)
		}
	}
	if !br.Appendable() {
		return
	}
	if b.Flags&binpazer.FlagHasCRC == 0 {
		v.add(ruleSeal, off, "block %d of an appendable file has no CRC seal", n)
	}
	if b.TypeID == binpazer.TypeBlockIndex {
		v.add(ruleAppendIndex, off, "block %d is a Block Index, which an appendable file does not carry", n)
	}
}

// appendFileChecks applies the appendable-file rules that need the whole walk.
func (v *validator) appendFileChecks(br *binpazer.Reader, clean bool) {
	if !br.Appendable() {
		return
	}
	if _, has := br.IndexOffset(); has {
		v.add(ruleAppendIndex, br.FileLength-16, "an appendable file has a footer")
	}
	if !clean || !br.Ended() {
		return
	}
	limit := br.FileLength
	if limit == binpazer.FileLengthUnknown {
		if br.InputSize() < 0 {
			v.skip(ruleEnd, "bytes after the End block need a seekable input")
			return
		}
		limit = uint64(br.InputSize())
	}
	if end := br.Offset(); end < limit {
		v.add(ruleEnd, end, "%d bytes follow the End block", limit-end)
	}
}
