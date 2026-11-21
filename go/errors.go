// errors.go holds ErrBadMagic.

package binpazer

import "errors"

// Sentinel errors returned by the reader. They may be wrapped with positional
// detail; match them with errors.Is.
var (
	// ErrBadMagic means the input does not start with the binpazer magic and signature bytes.
	ErrBadMagic = errors.New("binpazer: bad magic")

	// ErrCRCMismatch means a block's stored CRC-32C does not match its header and payload bytes.
	ErrCRCMismatch = errors.New("binpazer: CRC mismatch")

	// ErrTruncated means the input ends before data that a length, count, or offset field promises is present.
	ErrTruncated = errors.New("binpazer: truncated input")

	// ErrCapacityExceeded means a length or count field claims more than a structural or configured capacity allows.
	ErrCapacityExceeded = errors.New("binpazer: capacity exceeded")

	// ErrBadEnvelope means a compressed block's Compression Envelope is malformed: an invalid codec id, a set reserved field, a payload too short to hold the header.
	ErrBadEnvelope = errors.New("binpazer: bad compression envelope")

	// ErrShortBuffer means a caller-supplied destination is too small for the bytes a codec produced.
	ErrShortBuffer = errors.New("binpazer: destination buffer too small")

	// ErrCodecUnavailable means a compressed block names a codec this build does not have.
	ErrCodecUnavailable = errors.New("binpazer: codec unavailable")

	// ErrPending means the next block of an appendable file is not committed.
	ErrPending = errors.New("binpazer: block not committed")

	// ErrNotAppendable means the file does not set the appendable header flag.
	ErrNotAppendable = errors.New("binpazer: not an appendable file")

	// ErrEnded means an appendable file holds its End block and accepts no more blocks.
	ErrEnded = errors.New("binpazer: appendable file is ended")
)

// ErrTorn means the whole next block is in the file but its CRC seal is absent or incorrect.
var ErrTorn error = tornError{}

type tornError struct{}

func (tornError) Error() string { return "binpazer: torn block" }

func (tornError) Is(target error) bool { return target == ErrPending }
