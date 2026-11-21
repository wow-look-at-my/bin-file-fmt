// extract.go holds matchRangeErr.

package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// matchRangeErr reports a --type/--index selection that matched nothing.
func matchRangeErr(typeID uint16, k, n int) error {
	if n == 0 {
		return fmt.Errorf("no blocks of type %d in the input", typeID)
	}
	return fmt.Errorf("--index %d out of range: the input has %d block(s) of type %d", k, n, typeID)
}

// writePayload streams one block's payload to w. A compressed block is decoded
// unless `stored` asks for the bytes as they sit in the file -- the Compression
// Envelope and the codec's own output, which is what a format inspector wants
// and what nobody consuming a payload does.
func writePayload(br *binpazer.Reader, b *binpazer.Block, w io.Writer, stored bool) error {
	if stored || b.Flags&binpazer.FlagCompressed == 0 {
		_, err := br.WritePayloadTo(b, w)
		return err
	}
	r, err := br.DecompressedPayloadReader(b)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		return err
	}
	return br.SkipPayload(b)
}

// runExtract streams the payload of the k-th block (0-based, file order) with
// the given type id to w. Seekable inputs jump via Find/At -- straight through
// the Block Index when the file carries one. And non-seekable inputs fall back
// to a sequential walk.
func runExtract(br *binpazer.Reader, typeID uint16, k int, w io.Writer, stored bool) error {
	if k < 0 {
		return fmt.Errorf("--index must be >= 0")
	}
	if br.InputSize() >= 0 {
		offs, err := br.Find(typeID)
		if err != nil {
			return err
		}
		if k >= len(offs) {
			return matchRangeErr(typeID, k, len(offs))
		}
		b, err := br.At(offs[k])
		if err != nil {
			return err
		}
		return writePayload(br, b, w, stored)
	}
	seen := 0
	for {
		b, err := br.Next()
		if err == io.EOF {
			return matchRangeErr(typeID, k, seen)
		}
		if err != nil {
			return err
		}
		if b.TypeID == typeID {
			if seen == k {
				return writePayload(br, b, w, stored)
			}
			seen++
		}
		if err := br.SkipPayload(b); err != nil {
			return err
		}
	}
}

func init() {
	var (
		typeID   uint16
		index    int
		out      string
		stored   bool
		maxAlloc *int64
	)
	cmd := &cobra.Command{
		Use:     "extract <file.binpazer | ->",
		Aliases: []string{"cat"},
		Short:   "Extract one block's payload by type id (to stdout by default)",
		Long: `Extract the payload of a block selected by --type, streaming it to stdout
(or --out). Seekable inputs jump straight to the block via the Block Index
when the file has one; otherwise the blocks are walked. When several blocks
share the type, --index picks one (0-based, file order; see the find
command for the list). The CRC is verified while streaming when present.

A compressed block is decoded on the way out, so what you get is the block's
logical payload whatever codec produced it; --stored emits the Compression
Envelope and codec bytes exactly as they sit in the file.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			br, c, err := openReader(cmd, args[0], *maxAlloc)
			if err != nil {
				return err
			}
			if c != nil {
				defer c.Close()
			}
			if out == "-" {
				return runExtract(br, typeID, index, cmd.OutOrStdout(), stored)
			}
			f, err := os.Create(out)
			if err != nil {
				return err
			}
			if err := runExtract(br, typeID, index, f, stored); err != nil {
				f.Close()
				os.Remove(out) // do not leave a partial payload behind
				return err
			}
			return f.Close()
		},
	}
	cmd.Flags().Uint16Var(&typeID, "type", 0, "block type id to extract (required)")
	_ = cmd.MarkFlagRequired("type")
	cmd.Flags().IntVar(&index, "index", 0, "which matching block to extract (0-based, file order)")
	cmd.Flags().StringVarP(&out, "out", "o", "-", "output file, or - for stdout")
	cmd.Flags().BoolVar(&stored, "stored", false, "emit a compressed block's stored bytes (envelope + codec output) instead of decoding it")
	maxAlloc = maxAllocFlag(cmd)
	rootCmd.AddCommand(cmd)
}
