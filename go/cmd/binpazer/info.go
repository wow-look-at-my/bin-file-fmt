package main

import (
	"errors"
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
		// The envelope header sits at the front of the payload.
		codec := ""
		if b.Flags&binpazer.FlagCompressed != 0 {
			if c, cerr := br.Compression(b); cerr == nil {
				codec = fmt.Sprintf(" codec=%s ulen=%d", codecName(c.CodecID), c.UncompressedLen)
			}
		}
		fmt.Fprintf(w, "  [%d] type=%-6d %-14s flags=[%s] offset=%d len=%d%s\n",
			n, b.TypeID, tt.name(b.TypeID), strings.Join(displayFlagNames(b.Flags), ","),
			b.Offset(), b.PayloadLen, codec)
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

// runInfoJSON emits the same information as runInfo as one JSON object.
// has_index is recorded after the walk, so a stream that only discovers the
// footer at the end still reports it.
func runInfoJSON(br *binpazer.Reader, w io.Writer, recurse bool) error {
	tt := newTypeTable(br)
	out := infoJSON{
		VersionMajor: br.VersionMajor,
		VersionMinor: br.VersionMinor,
		WriterName:   br.WriterName,
		WriterGUID:   br.WriterGUID.String(),
		Types:        make([]typeJSON, 0, len(br.Types)),
		Blocks:       []blockJSON{},
	}
	if br.FileLength != binpazer.FileLengthUnknown {
		fl := br.FileLength
		out.FileLength = &fl
	}
	for _, t := range br.Types {
		out.Types = append(out.Types, typeJSON{TypeID: t.TypeID, GUID: t.GUID.String(), Name: t.Name})
	}
	n := 0
	for {
		b, err := br.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		bj := topBlockJSON(tt, b, n)
		if recurse && tt.isGroup(b.TypeID) {
			payload, rerr := br.ReadPayload(b)
			switch {
			case rerr == nil:
				bj.Children, bj.Error = childrenJSON(tt, payload, b.Offset()+8, 1, false)
			case errors.Is(rerr, binpazer.ErrCRCMismatch):
				bj.Error = rerr.Error()
				if err := br.SkipPayload(b); err != nil {
					return err
				}
			default:
				return rerr
			}
		} else if err := br.SkipPayload(b); err != nil {
			return err
		}
		out.Blocks = append(out.Blocks, bj)
		n++
	}
	out.HasIndex = br.HasIndex()
	return writeJSON(w, out)
}

func init() {
	var recurse, asJSON bool
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
			if asJSON {
				return runInfoJSON(br, cmd.OutOrStdout(), recurse)
			}
			return runInfo(br, cmd.OutOrStdout(), recurse)
		},
	}
	cmd.Flags().BoolVar(&recurse, "recurse", false,
		"descend into group blocks and list nested blocks indented")
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"emit one machine-readable JSON object (nested blocks appear as children)")
	maxAlloc = maxAllocFlag(cmd)
	rootCmd.AddCommand(cmd)
}
