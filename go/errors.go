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

	// ErrBadEnvelope means a compressed block's Compression Envelope is
	// malformed: an invalid codec id, a set reserved field, a payload too short
	// to hold the header, or a decode whose length disagrees with the
	// envelope's uncompressed_length.
	ErrBadEnvelope = errors.New("binpazer: bad compression envelope")

	// ErrShortBuffer means a caller-supplied destination is too small for the
	// bytes a codec produced. Size a compression destination with
	// CompressBound and a decompression destination with the envelope's
	// UncompressedLen.
	ErrShortBuffer = errors.New("binpazer: destination buffer too small")

	// ErrCodecUnavailable means a compressed block names a codec this build
	// does not have. The block can still be walked, skipped and copied
	// verbatim; only its payload is uninterpretable. Register the codec with
	// RegisterCodec.
	ErrCodecUnavailable = errors.New("binpazer: codec unavailable")
)
