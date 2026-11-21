// appendstate.go holds appendState, what a read command reports about an appendable file.

package main

import (
	"fmt"

	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// appendState says where an appendable file stands once a walk is over.
type appendState struct {
	Ended bool `json:"ended"`
	// InProgress is set when bytes follow the last committed block.
	InProgress *inProgress `json:"in_progress,omitempty"`
}

// inProgress locates the bytes after the last committed block. Bytes is zero when the input size is unknown.
type inProgress struct {
	Offset uint64 `json:"offset"`
	Bytes  uint64 `json:"bytes,omitempty"`
}

// appendStateOf reads the state from a reader whose walk has ended. A file that is not appendable has none.
func appendStateOf(br *binpazer.Reader) *appendState {
	if !br.Appendable() {
		return nil
	}
	st := &appendState{Ended: br.Ended() || br.FileLength != binpazer.FileLengthUnknown}
	if size := br.InputSize(); !st.Ended && size >= 0 && uint64(size) > br.Offset() {
		st.InProgress = &inProgress{Offset: br.Offset(), Bytes: uint64(size) - br.Offset()}
	}
	return st
}

func (s *appendState) String() string {
	switch {
	case s.Ended:
		return "ended"
	case s.InProgress != nil:
		return fmt.Sprintf("open (%d bytes in progress at offset %d)", s.InProgress.Bytes, s.InProgress.Offset)
	}
	return "open"
}
