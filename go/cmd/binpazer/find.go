// find.go holds findMatch.

package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// findMatch describes one block located by find.
type findMatch struct {
	Index      int      `json:"index"`
	Offset     uint64   `json:"offset"`
	PayloadLen uint64   `json:"payload_len"`
	Flags      []string `json:"flags,omitempty"`
}

// findResult is the machine-readable shape of a find run.
type findResult struct {
	TypeID  uint16      `json:"type_id"`
	Matches []findMatch `json:"matches"`
}

// runFind lists every block with the given type id (offset, payload size,
// flags), via Find/At on seekable inputs (using the Block Index when present)
// and a sequential walk on streams.
func runFind(br *binpazer.Reader, typeID uint16) (*findResult, error) {
	res := &findResult{TypeID: typeID, Matches: []findMatch{}}
	add := func(b *binpazer.Block) {
		res.Matches = append(res.Matches, findMatch{
			Index:      len(res.Matches),
			Offset:     b.Offset(),
			PayloadLen: b.PayloadLen,
			Flags:      flagNames(b.Flags),
		})
	}
	if br.InputSize() >= 0 {
		offs, err := br.Find(typeID)
		if err != nil {
			return nil, err
		}
		for _, off := range offs {
			b, err := br.At(off)
			if err != nil {
				return nil, err
			}
			add(b)
		}
		return res, nil
	}
	for {
		b, err := walkNext(br)
		if err == io.EOF {
			return res, nil
		}
		if err != nil {
			return nil, err
		}
		if b.TypeID == typeID {
			add(b)
		}
		if err := br.SkipPayload(b); err != nil {
			return nil, err
		}
	}
}

func init() {
	var (
		typeID   uint16
		asJSON   bool
		maxAlloc *int64
	)
	cmd := &cobra.Command{
		Use:   "find <file.binpazer | ->",
		Short: "List blocks with a given type id (offset, size, flags)",
		Long: `List every block with the given --type id, jumping through the Block Index
when the file has one. Each match's printed index is the value to pass to
extract --index. Exits nonzero when no block matches.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			br, c, err := openReader(cmd, args[0], *maxAlloc)
			if err != nil {
				return err
			}
			if c != nil {
				defer c.Close()
			}
			res, err := runFind(br, typeID)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if asJSON {
				if err := writeJSON(w, res); err != nil {
					return err
				}
			} else {
				for _, m := range res.Matches {
					fmt.Fprintf(w, "[%d] offset=%d len=%d flags=[%s]\n",
						m.Index, m.Offset, m.PayloadLen, strings.Join(m.Flags, ","))
				}
			}
			if len(res.Matches) == 0 {
				return fmt.Errorf("no blocks of type %d in the input", typeID)
			}
			return nil
		},
	}
	cmd.Flags().Uint16Var(&typeID, "type", 0, "block type id to find (required)")
	_ = cmd.MarkFlagRequired("type")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable JSON")
	maxAlloc = maxAllocFlag(cmd)
	rootCmd.AddCommand(cmd)
}
