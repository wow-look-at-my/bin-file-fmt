package binpazer

import "errors"

// Sentinel errors returned by the reader. They may be wrapped with positional
// detail; match them with errors.Is.
var (
	// ErrBadMagic means the input does not start with the binpazer magic and
	// signature bytes.
	ErrBadMagic = errors.New("binpazer: bad magic")

	// ErrCRCMismatch means a block's stored CRC-32C does not match its header
	// and payload bytes.
	ErrCRCMismatch = errors.New("binpazer: CRC mismatch")

	// ErrTruncated means the input ends before data that a length, count, or
	// offset field promises is present.
	ErrTruncated = errors.New("binpazer: truncated input")

	// ErrCapacityExceeded means a length or count field claims more than a
	// structural or configured capacity allows: more Block Type Table entries
	// than the table's own byte length can hold, or a claim larger than the
	// allocation cap for non-seekable input (see NewReaderLimited).
	ErrCapacityExceeded = errors.New("binpazer: capacity exceeded")
)
