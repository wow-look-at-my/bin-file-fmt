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
func runUnpack(br *binpazer.Reader, outDir string) (*Manifest, error) {
	m := &Manifest{
		WriterGUID: br.WriterGUID.String(),
		WriterName: br.WriterName,
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
		name := fmt.Sprintf("block%d.bin", i)
		codec, err := streamBlockToFile(br, b, filepath.Join(outDir, name))
		if err != nil {
			return nil, err
		}
		m.Blocks = append(m.Blocks, ManifestBlock{
			TypeID: b.TypeID, Flags: flagNames(b.Flags), Codec: codec, File: name,
		})
		i++
	}
	// Recorded after the walk: a non-seekable input only discovers the footer
	// (and thus the index) when the walk reaches it.
	m.Index = br.HasIndex()
	return m, nil
}

// streamBlockToFile streams the block's payload straight to path (64 KiB
// chunks, CRC verified when present) so no payload is ever buffered whole, and
// returns the codec name for a compressed block ("" otherwise).
//
// A compressed block is written out DECODED: the file is named for the block's
// payload, so envelope bytes in it would be a lie, and the manifest's `codec`
// is what lets pack put the block back exactly as it was. On error the partial
// file is removed.
func streamBlockToFile(br *binpazer.Reader, b *binpazer.Block, path string) (string, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if b.Flags&binpazer.FlagCompressed != 0 {
		c, r, err := br.DecompressedPayload(b)
		if err != nil {
			return fail(err)
		}
		if _, err := io.Copy(f, r); err != nil {
			return fail(err)
		}
		if err := br.SkipPayload(b); err != nil {
			return fail(err)
		}
		return codecName(c.CodecID), f.Close()
	}
	if _, err := br.WritePayloadTo(b, f); err != nil {
		return fail(err)
	}
	return "", f.Close()
}

func init() {
	var outDir string
	var maxAlloc *int64
	cmd := &cobra.Command{
		Use:   "unpack <file.binpazer | ->",
		Short: "Extract a file into a JSON manifest + payload files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outDir == "" {
				return fmt.Errorf("--dir is required")
			}
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				return err
			}
			br, c, err := openReader(cmd, args[0], *maxAlloc)
			if err != nil {
				return err
			}
			if c != nil {
				defer c.Close()
			}
			m, err := runUnpack(br, outDir)
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
	maxAlloc = maxAllocFlag(cmd)
	rootCmd.AddCommand(cmd)
}
