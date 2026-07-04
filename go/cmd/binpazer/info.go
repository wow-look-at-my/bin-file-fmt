package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// runInfo prints a human-readable summary: header, the block type table, and
// a list of blocks with their flags, offsets, and sizes. With recurse, group
// blocks are descended and their children listed indented.
func runInfo(br *binpazer.Reader, w io.Writer, recurse bool) error {
	fmt.Fprintf(w, "binpazer v%d.%d\n", br.VersionMajor, br.VersionMinor)
	fmt.Fprintf(w, "writer:      %s (%s)\n", br.WriterName, br.WriterGUID)
	if br.FileLength == binpazer.FileLengthUnknown {
		fmt.Fprintln(w, "file_length: (streaming/unknown)")
	} else {
		fmt.Fprintf(w, "file_length: %d\n", br.FileLength)
	}
	fmt.Fprintf(w, "has_index:   %v\n", br.HasIndex())

	tt := newTypeTable(br)
	fmt.Fprintf(w, "\nblock types (%d):\n", len(br.Types))
	for _, t := range br.Types {
		fmt.Fprintf(w, "  %-6d %s  %s\n", t.TypeID, t.GUID, t.Name)
	}

	fmt.Fprintln(w, "\nblocks:")
	n := 0
	for {
		b, err := br.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "  [%d] type=%-6d %-14s flags=[%s] offset=%d len=%d\n",
			n, b.TypeID, tt.name(b.TypeID), strings.Join(flagNames(b.Flags), ","), b.Offset(), b.PayloadLen)
		if recurse && tt.isGroup(b.TypeID) {
			err = descendGroup(br, w, tt, b, fmt.Sprintf("%d", n), false)
		} else {
			err = br.SkipPayload(b)
		}
		if err != nil {
			return err
		}
		n++
	}
	return nil
}

func init() {
	var recurse bool
	var maxAlloc *int64
	cmd := &cobra.Command{
		Use:   "info <file.binpazer | ->",
		Short: "Show header, block type table, and block listing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			br, c, err := openReader(cmd, args[0], *maxAlloc)
			if err != nil {
				return err
			}
			if c != nil {
				defer c.Close()
			}
			return runInfo(br, cmd.OutOrStdout(), recurse)
		},
	}
	cmd.Flags().BoolVar(&recurse, "recurse", false,
		"descend into group blocks and list nested blocks indented")
	maxAlloc = maxAllocFlag(cmd)
	rootCmd.AddCommand(cmd)
}
