package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// runDump hex-dumps block payloads. If only >= 0, only that block index is dumped.
func runDump(rs io.ReadSeeker, w io.Writer, only int) error {
	br, err := binpazer.NewReader(rs)
	if err != nil {
		return err
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
		if only >= 0 && n != only {
			if err := br.SkipPayload(b); err != nil {
				return err
			}
			n++
			continue
		}
		fmt.Fprintf(w, "block %d (type %d, %d bytes):\n", n, b.TypeID, b.PayloadLen)
		d := hex.Dumper(w) // streams the dump; no whole-payload buffer
		if _, err := br.WritePayloadTo(b, d); err != nil {
			d.Close()
			return err
		}
		if err := d.Close(); err != nil {
			return err
		}
		n++
	}
	return nil
}

func init() {
	var only int
	cmd := &cobra.Command{
		Use:   "dump <file.binpazer>",
		Short: "Hex-dump block payloads",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			return runDump(f, cmd.OutOrStdout(), only)
		},
	}
	cmd.Flags().IntVar(&only, "block", -1, "dump only this block index (default: all)")
	rootCmd.AddCommand(cmd)
}
