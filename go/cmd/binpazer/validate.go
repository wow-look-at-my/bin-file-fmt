// validate.go holds runValidate.

package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

func isPredefined(id uint16) bool {
	return id >= binpazer.TypeEnd && id <= binpazer.TypeBlockTypeTable
}

// runValidate checks the input against every rule of SPEC.md's "Validation"
// checklist. Structural parse failures become findings too. The result is
// always usable. Rules that need a seekable input are reported as skipped on
// streams instead of silently passing. Payloads are streamed, never buffered.
func runValidate(r io.Reader, maxAlloc int64) *validateResult {
	v := newValidator()
	br, err := binpazer.NewReaderLimited(r, maxAlloc)
	if err != nil {
		v.fatalInit(err)
		return v.finish()
	}

	v.bttChecks(br)  // duplicate / invalid type ids in the table
	v.utf8Checks(br) // string UTF-8 validity + first_block_offset alignment

	if rs, ok := r.(io.ReadSeeker); ok && br.InputSize() >= 0 {
		v.rawStructuralChecks(rs, br) // reserved fields + string terminators
	} else {
		v.skip(ruleStringTerm, "requires seekable input")
		v.skip(ruleReservedZero, "header/table reserved fields require seekable input; block flag bits still checked")
		v.skip(ruleIndex, "requires seekable input")
	}

	indexBlockSeen, clean := v.walk(br)
	v.appendFileChecks(br, clean)
	v.lengthChecks(br, clean)
	if br.InputSize() >= 0 {
		v.indexChecks(br, indexBlockSeen)
	}
	return v.finish()
}

func init() {
	var asJSON bool
	var maxAlloc *int64
	cmd := &cobra.Command{
		Use:   "validate <file.binpazer | ->",
		Short: "Check a file against the spec's validation checklist",
		Long: `Check a file against every rule of SPEC.md's Validation checklist:
magic/signature, first-block-is-BTT, duplicate or invalid type ids,
unknown block types, reserved fields and flag bits, 8-byte alignment,
file_length consistency, string NUL termination and UTF-8, CRC trailers,
and footer/Block Index consistency.

Each violation prints as one finding (rule id + offset + detail) and the
exit status is nonzero when any finding exists. On non-seekable stdin,
rules that need to revisit bytes are reported as skipped rather than
silently passing. --json emits {ok, findings, rules} with a per-rule
pass/fail/skipped status.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, c, err := inputReader(cmd, args[0])
			if err != nil {
				return err
			}
			if c != nil {
				defer c.Close()
			}
			res := runValidate(r, *maxAlloc)
			w := cmd.OutOrStdout()
			if asJSON {
				if err := writeJSON(w, res); err != nil {
					return err
				}
				if !res.OK {
					return fmt.Errorf("validation failed: %d finding(s)", len(res.Findings))
				}
				return nil
			}
			for _, rs := range res.Rules {
				if rs.Status == "skipped" && !strings.HasPrefix(rs.Reason, "not evaluated") {
					fmt.Fprintf(w, "skipped: %s (%s)\n", rs.Rule, rs.Reason)
				}
			}
			if p := res.InProgress; p != nil {
				fmt.Fprintf(w, "in progress: the writer has not ended the file; unfinished bytes start at offset %d\n", p.Offset)
			}
			if res.OK {
				fmt.Fprintln(w, "OK: file is spec-conformant")
				return nil
			}
			lines := make([]string, len(res.Findings))
			for i, f := range res.Findings {
				lines[i] = fmt.Sprintf("rule=%s offset=%d: %s", f.Rule, f.Offset, f.Detail)
			}
			return fmt.Errorf("%d finding(s):\n  %s", len(res.Findings), strings.Join(lines, "\n  "))
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable JSON ({ok, findings, rules})")
	maxAlloc = maxAllocFlag(cmd)
	rootCmd.AddCommand(cmd)
}
