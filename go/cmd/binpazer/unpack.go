package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// runUnpack reads a binpazer file and writes each block's payload to outDir as
// blockN.bin, returning a Manifest that references them. Re-packing the manifest
// reproduces the file (the index is regenerated from "index": true).
func runUnpack(r io.Reader, outDir string) (*Manifest, error) {
	br, err := binpazer.NewReader(r)
	if err != nil {
		return nil, err
	}
	m := &Manifest{
		WriterGUID: br.WriterGUID.String(),
		WriterName: br.WriterName,
		Index:      br.HasIndex(),
	}
	for _, t := range br.Types {
		if t.TypeID == binpazer.TypeBlockTypeTable {
			continue // re-added automatically on pack
		}
		m.Types = append(m.Types, ManifestType{TypeID: t.TypeID, GUID: t.GUID.String(), Name: t.Name})
	}
	i := 0
	for {
		b, err := br.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if b.TypeID == binpazer.TypeBlockIndex {
			if err := br.SkipPayload(b); err != nil { // regenerated via "index": true
				return nil, err
			}
			continue
		}
		p, err := br.ReadPayload(b)
		if err != nil {
			return nil, err
		}
		name := fmt.Sprintf("block%d.bin", i)
		if err := os.WriteFile(filepath.Join(outDir, name), p, 0o644); err != nil {
			return nil, err
		}
		m.Blocks = append(m.Blocks, ManifestBlock{TypeID: b.TypeID, Flags: flagNames(b.Flags), File: name})
		i++
	}
	return m, nil
}

func init() {
	var outDir string
	cmd := &cobra.Command{
		Use:   "unpack <file.binpazer>",
		Short: "Extract a file into a JSON manifest + payload files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outDir == "" {
				return fmt.Errorf("--dir is required")
			}
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				return err
			}
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			m, err := runUnpack(f, outDir)
			if err != nil {
				return err
			}
			raw, err := json.MarshalIndent(m, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(outDir, "manifest.json"), append(raw, '\n'), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "unpacked %d block(s) to %s\n", len(m.Blocks), outDir)
			return nil
		},
	}
	cmd.Flags().StringVarP(&outDir, "dir", "d", "", "output directory (required)")
	rootCmd.AddCommand(cmd)
}
