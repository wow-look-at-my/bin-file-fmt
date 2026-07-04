package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

func isPredefined(id uint16) bool {
	return id >= binpazer.TypeGroup && id <= binpazer.TypeBlockTypeTable
}

// runValidate checks a file against the spec. It returns a list of problems
// (empty means conformant) or a fatal structural error (bad magic/header/BTT).
func runValidate(rs io.ReadSeeker) ([]string, error) {
	br, err := binpazer.NewReader(rs)
	if err != nil {
		return nil, err
	}
	size := br.InputSize() // rs is seekable, so the reader learned it

	var problems []string
	if br.FileLength != binpazer.FileLengthUnknown && int64(br.FileLength) != size {
		problems = append(problems, fmt.Sprintf("file_length=%d but file is %d bytes", br.FileLength, size))
	}
	n := 0
	for {
		b, err := br.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("block %d: %v", n, err))
			break
		}
		if !b.Known && !isPredefined(b.TypeID) {
			problems = append(problems, fmt.Sprintf("block %d: type_id %d not in the block type table", n, b.TypeID))
		}
		// A CRC-carrying block is streamed through the checksum in 64 KiB
		// chunks; one without a CRC is skipped without reading its payload.
		// Either way nothing is buffered and truncation still surfaces.
		if b.Flags&binpazer.FlagHasCRC != 0 {
			_, err = br.WritePayloadTo(b, io.Discard)
		} else {
			err = br.SkipPayload(b)
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("block %d (type %d): %v", n, b.TypeID, err))
			break
		}
		n++
	}
	return problems, nil
}

func init() {
	cmd := &cobra.Command{
		Use:   "validate <file.binpazer>",
		Short: "Check a file for spec conformance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			problems, err := runValidate(f)
			if err != nil {
				return fmt.Errorf("not a valid binpazer file: %w", err)
			}
			if len(problems) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "OK: file is spec-conformant")
				return nil
			}
			return fmt.Errorf("%d problem(s):\n  %s", len(problems), strings.Join(problems, "\n  "))
		},
	}
	rootCmd.AddCommand(cmd)
}
