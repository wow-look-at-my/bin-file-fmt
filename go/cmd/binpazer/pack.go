package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// payload resolves a block's content from data / data_base64 / file.
func (b ManifestBlock) payload(baseDir string) ([]byte, error) {
	n := 0
	for _, set := range []bool{b.Data != "", b.DataBase64 != "", b.File != ""} {
		if set {
			n++
		}
	}
	if n > 1 {
		return nil, fmt.Errorf("set at most one of data/data_base64/file")
	}
	switch {
	case b.Data != "":
		return []byte(b.Data), nil
	case b.DataBase64 != "":
		return base64.StdEncoding.DecodeString(b.DataBase64)
	case b.File != "":
		return os.ReadFile(filepath.Join(baseDir, b.File))
	}
	return nil, nil
}

// runPack writes the manifest as a binpazer file to w. File paths in the
// manifest are resolved relative to baseDir.
func runPack(m *Manifest, baseDir string, w io.Writer) error {
	wg, err := binpazer.ParseGUID(m.WriterGUID)
	if err != nil {
		return fmt.Errorf("writer_guid: %w", err)
	}
	defs := make([]binpazer.TypeDef, 0, len(m.Types))
	for _, t := range m.Types {
		g, err := binpazer.ParseGUID(t.GUID)
		if err != nil {
			return fmt.Errorf("type %d: %w", t.TypeID, err)
		}
		defs = append(defs, binpazer.TypeDef{TypeID: t.TypeID, GUID: g, Name: t.Name})
	}
	bw, err := binpazer.NewWriter(w, wg, m.WriterName, defs)
	if err != nil {
		return err
	}
	for i, blk := range m.Blocks {
		flags, err := parseFlags(blk.Flags)
		if err != nil {
			return fmt.Errorf("block %d: %w", i, err)
		}
		p, err := blk.payload(baseDir)
		if err != nil {
			return fmt.Errorf("block %d: %w", i, err)
		}
		if err := bw.Block(blk.TypeID, flags, p); err != nil {
			return fmt.Errorf("block %d: %w", i, err)
		}
	}
	if m.Index {
		if err := bw.WriteIndex(); err != nil {
			return err
		}
	}
	return bw.End()
}

func init() {
	var out string
	cmd := &cobra.Command{
		Use:   "pack <manifest.json>",
		Short: "Build a binpazer file from a JSON manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if out == "" {
				return fmt.Errorf("--out is required")
			}
			raw, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var m Manifest
			if err := json.Unmarshal(raw, &m); err != nil {
				return fmt.Errorf("parsing manifest: %w", err)
			}
			f, err := os.Create(out)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := runPack(&m, filepath.Dir(args[0]), f); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "output file (required)")
	rootCmd.AddCommand(cmd)
}
