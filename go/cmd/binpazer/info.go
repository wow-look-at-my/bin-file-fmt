package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// runInfo prints a human-readable summary: header, the block type table, and a
// list of blocks with their flags, offsets, and sizes.
func runInfo(rs io.ReadSeeker, w io.Writer) error {
	br, err := binpazer.NewReader(rs)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "binpazer v%d.%d\n", br.VersionMajor, br.VersionMinor)
	fmt.Fprintf(w, "writer:      %s (%s)\n", br.WriterName, br.WriterGUID)
	if br.FileLength == binpazer.FileLengthUnknown {
		fmt.Fprintln(w, "file_length: (streaming/unknown)")
	} else {
		fmt.Fprintf(w, "file_length: %d\n", br.FileLength)
	}
	fmt.Fprintf(w, "has_index:   %v\n", br.HasIndex())

	names := map[uint16]string{}
	fmt.Fprintf(w, "\nblock types (%d):\n", len(br.Types))
	for _, t := range br.Types {
		names[t.TypeID] = t.Name
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
		name := names[b.TypeID]
		if name == "" {
			name = predefinedName(b.TypeID)
		}
		fmt.Fprintf(w, "  [%d] type=%-6d %-14s flags=[%s] offset=%d len=%d\n",
			n, b.TypeID, name, strings.Join(flagNames(b.Flags), ","), b.Offset(), b.PayloadLen)
		if err := br.SkipPayload(b); err != nil {
			return err
		}
		n++
	}
	return nil
}

func init() {
	cmd := &cobra.Command{
		Use:   "info <file.binpazer>",
		Short: "Show header, block type table, and block listing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			return runInfo(f, cmd.OutOrStdout())
		},
	}
	rootCmd.AddCommand(cmd)
}
