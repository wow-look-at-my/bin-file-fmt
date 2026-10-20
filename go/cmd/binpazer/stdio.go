package main

import (
	"encoding/json"
	"io"
	"os"

	"github.com/spf13/cobra"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// nonSeekable hides an input's Seek method so the binpazer reader takes its capped, streaming path.
type nonSeekable struct{ io.Reader }

// inputReader resolves a positional file argument. "-" means the command's
// stdin: when stdin is genuinely seekable (redirected from a file) it is used
// as-is. The reader can learn the input size and use the block index. This
// also covers otherwise its Seeker is hidden and the reader streams under
// --max-alloc.
func inputReader(cmd *cobra.Command, path string) (io.Reader, io.Closer, error) {
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, err
		}
		return f, f, nil
	}
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok {
		if _, err := f.Seek(0, io.SeekCurrent); err != nil {
			return nonSeekable{f}, nil, nil
		}
	}
	return in, nil, nil
}

// openReader opens a binpazer reader over a file argument, honoring "-" and
// the command's --max-alloc cap. The caller must Close the returned closer
// when it is non-nil.
func openReader(cmd *cobra.Command, path string, maxAlloc int64) (*binpazer.Reader, io.Closer, error) {
	r, c, err := inputReader(cmd, path)
	if err != nil {
		return nil, nil, err
	}
	br, err := binpazer.NewReaderLimited(r, maxAlloc)
	if err != nil {
		if c != nil {
			c.Close()
		}
		return nil, nil, err
	}
	return br, c, nil
}

// writeJSON emits v as indented JSON (snake_case field names come from the
// struct tags) with a trailing newline.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func maxAllocFlag(cmd *cobra.Command) *int64 {
	v := new(int64)
	cmd.Flags().Int64Var(v, "max-alloc", 0,
		"cap in bytes on any single length/count claim when the input is not seekable (0 = 1 GiB)")
	return v
}
