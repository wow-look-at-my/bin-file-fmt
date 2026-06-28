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
	size, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	br, err := binpazer.NewReader(rs)
	if err != nil {
		return nil, err
	}

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
		if _, err := br.ReadPayload(b); err != nil { // verifies CRC when present
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
