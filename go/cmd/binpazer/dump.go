// dump.go holds runDump.

package main

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// runDump hex-dumps block payloads. If only >= 0, only that block index is
// dumped. With recurse, group blocks are descended: their children are dumped
// individually instead of the group payload appearing as one opaque blob.
func runDump(br *binpazer.Reader, w io.Writer, only int, recurse bool) error {
	tt := newTypeTable(br)
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
		if recurse && tt.isGroup(b.TypeID) {
			fmt.Fprintf(w, "block %d (type %d, %d bytes): group\n", n, b.TypeID, b.PayloadLen)
			if err := descendGroup(br, w, tt, b, fmt.Sprintf("%d", n), true); err != nil {
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
	if only >= 0 && only >= n {
		return fmt.Errorf("block index %d out of range: the file has %d block(s)", only, n)
	}
	return nil
}

// runDumpJSON emits one JSON object with a blocks array; each block carries
// its payload as base64 (children instead, for groups with recurse). Unlike
// the hex path this buffers one payload at a time, bounded by the input size
// or --max-alloc.
func runDumpJSON(br *binpazer.Reader, w io.Writer, only int, recurse bool) error {
	tt := newTypeTable(br)
	out := dumpJSON{Blocks: []blockJSON{}}
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
		bj := topBlockJSON(tt, b, n)
		payload, err := br.ReadPayload(b)
		if err != nil {
			return err
		}
		if recurse && tt.isGroup(b.TypeID) {
			bj.Children, bj.Error = childrenJSON(tt, payload, b.Offset()+8, 1, true)
		} else {
			bj.PayloadB64 = base64.StdEncoding.EncodeToString(payload)
		}
		out.Blocks = append(out.Blocks, bj)
		n++
	}
	if only >= 0 && only >= n {
		return fmt.Errorf("block index %d out of range: the file has %d block(s)", only, n)
	}
	return writeJSON(w, out)
}

func init() {
	var only int
	var recurse, asJSON bool
	var maxAlloc *int64
	cmd := &cobra.Command{
		Use:   "dump <file.binpazer | ->",
		Short: "Hex-dump block payloads",
		Long: `Hex-dump block payloads (all blocks, or one selected with --block).

--json emits one JSON object instead: a blocks array whose entries carry
their payload as base64 in payload_base64 (with --recurse, group blocks
carry a children array of nested blocks instead of an opaque payload).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			br, c, err := openReader(cmd, args[0], *maxAlloc)
			if err != nil {
				return err
			}
			if c != nil {
				defer c.Close()
			}
			if asJSON {
				return runDumpJSON(br, cmd.OutOrStdout(), only, recurse)
			}
			return runDump(br, cmd.OutOrStdout(), only, recurse)
		},
	}
	cmd.Flags().IntVar(&only, "block", -1, "dump only this block index (default: all)")
	cmd.Flags().BoolVar(&recurse, "recurse", false,
		"descend into group blocks and dump nested blocks individually")
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"emit one machine-readable JSON object (payloads as base64)")
	maxAlloc = maxAllocFlag(cmd)
	rootCmd.AddCommand(cmd)
}
