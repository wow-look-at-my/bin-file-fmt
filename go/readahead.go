// readahead.go holds shortFirstReader, which keeps a seek cheap when the caller wants one small block.

package binpazer

import "io"

// seekReadAhead is the most the first read after a seek asks the input for.
const seekReadAhead = 4096

// shortFirstReader caps the first read after a seek.
type shortFirstReader struct {
	r     io.Reader
	short bool
}

func (s *shortFirstReader) Read(p []byte) (int, error) {
	if s.short {
		s.short = false
		if len(p) > seekReadAhead {
			p = p[:seekReadAhead]
		}
	}
	return s.r.Read(p)
}
