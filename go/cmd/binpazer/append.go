// append.go holds the append command, which adds a block to an open appendable file.

package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// appendOptions is what one append run adds to the file.
type appendOptions struct {
	hasBlock bool
	typeID   uint16
	flags    []string
	codec    string
	payload  []byte
	end      bool
}

// runAppend resumes the file, adds the block if there is one, and ends the file if asked.
func runAppend(rw io.ReadWriteSeeker, o appendOptions, w io.Writer) error {
	bw, types, err := binpazer.ResumeAppendWriter(rw)
	if err != nil {
		return err
	}
	if n := bw.SealedTail(); n > 0 {
		fmt.Fprintf(w, "sealed a torn tail: %d stale bytes followed the last committed block\n", n)
	}
	if o.hasBlock {
		known := false
		for _, t := range types {
			known = known || t.TypeID == o.typeID
		}
		if !known {
			return fmt.Errorf("type %d is not in the file's block type table", o.typeID)
		}
		flags, err := parseFlags(o.flags)
		if err != nil {
			return err
		}
		off := bw.Offset()
		if o.codec != "" {
			codec, err := parseCodec(o.codec)
			if err != nil {
				return err
			}
			err = bw.CompressedBlock(o.typeID, flags, codec, o.payload)
			if err != nil {
				return err
			}
		} else if err := bw.Block(o.typeID, flags, o.payload); err != nil {
			return err
		}
		fmt.Fprintf(w, "appended a type %d block at offset %d\n", o.typeID, off)
	}
	if o.end {
		if err := bw.End(); err != nil {
			return err
		}
		fmt.Fprintln(w, "ended the file")
	}
	return nil
}

func init() {
	var (
		typeID          uint16
		flags           []string
		codec           string
		data, dataB64   string
		payloadFile     string
		end             bool
		sourceFlagNames = []string{"data", "data-base64", "payload"}
	)
	cmd := &cobra.Command{
		Use:   "append <file.binpazer>",
		Short: "Add a block to an open appendable file, or end the file",
		Long: `Add one block to an appendable file that a writer left open. The block is
sealed with a CRC and is committed when the command returns, so a
"binpazer follow" on the same file prints it.

Create the file first with "binpazer pack" and a manifest that sets
"appendable": true and "open": true.

--type selects the block type, which must be in the file's block type table.
The payload comes from --data (text), --data-base64, or --payload (a file, or
- for stdin); with none of them the block is empty. --end writes the End
block: the file is then closed for good.

If an interrupted writer left a torn tail, append seals it inside Padding
blocks before it writes. With no --type and no --end, that repair is all the
command does.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o := appendOptions{hasBlock: cmd.Flags().Changed("type"), typeID: typeID, flags: flags, codec: codec, end: end}
			set := 0
			for _, n := range sourceFlagNames {
				if cmd.Flags().Changed(n) {
					set++
				}
			}
			if set > 1 {
				return fmt.Errorf("set at most one of --%s", strings.Join(sourceFlagNames, ", --"))
			}
			if !o.hasBlock && (set > 0 || cmd.Flags().Changed("flag") || codec != "") {
				return fmt.Errorf("--type is required to append a block")
			}
			var err error
			switch {
			case cmd.Flags().Changed("data"):
				o.payload = []byte(data)
			case cmd.Flags().Changed("data-base64"):
				o.payload, err = base64.StdEncoding.DecodeString(dataB64)
			case payloadFile == "-":
				o.payload, err = io.ReadAll(cmd.InOrStdin())
			case cmd.Flags().Changed("payload"):
				o.payload, err = os.ReadFile(payloadFile)
			}
			if err != nil {
				return err
			}
			f, err := os.OpenFile(args[0], os.O_RDWR, 0)
			if err != nil {
				return err
			}
			if err := runAppend(f, o, cmd.OutOrStdout()); err != nil {
				f.Close()
				return err
			}
			return f.Close()
		},
	}
	cmd.Flags().Uint16Var(&typeID, "type", 0, "type id of the block to append")
	cmd.Flags().StringSliceVar(&flags, "flag", nil, "block flag to set; repeat it or separate names with commas (has_crc is always set)")
	cmd.Flags().StringVar(&codec, "codec", "", "compress the payload with this codec")
	cmd.Flags().StringVar(&data, "data", "", "payload as text")
	cmd.Flags().StringVar(&dataB64, "data-base64", "", "payload as base64")
	cmd.Flags().StringVar(&payloadFile, "payload", "", "file that holds the payload, or - for stdin")
	cmd.Flags().BoolVar(&end, "end", false, "write the End block after the block, which closes the file for good")
	rootCmd.AddCommand(cmd)
}
