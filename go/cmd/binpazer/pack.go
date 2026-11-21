// pack.go holds payload.

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
		// A `codec` compresses the block; the manifest always holds the block's
		// logical bytes, so pack is what produces the Compression Envelope.
		if blk.Codec != "" {
			codec, err := parseCodec(blk.Codec)
			if err != nil {
				return fmt.Errorf("block %d: %w", i, err)
			}
			if err := bw.CompressedBlock(blk.TypeID, flags, codec, p); err != nil {
				return fmt.Errorf("block %d: %w", i, err)
			}
			continue
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

type memFile struct {
	buf []byte
	pos int64
}

func (m *memFile) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if end > int64(len(m.buf)) {
		if end > int64(cap(m.buf)) {
			m.buf = append(m.buf[:cap(m.buf)], make([]byte, int(end)-cap(m.buf))...)
		}
		m.buf = m.buf[:end]
	}
	copy(m.buf[m.pos:], p)
	m.pos = end
	return len(p), nil
}

func (m *memFile) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = m.pos
	case io.SeekEnd:
		base = int64(len(m.buf))
	default:
		return 0, fmt.Errorf("memFile: invalid whence %d", whence)
	}
	if base+offset < 0 {
		return 0, fmt.Errorf("memFile: negative seek position")
	}
	m.pos = base + offset
	return m.pos, nil
}

func init() {
	var out string
	cmd := &cobra.Command{
		Use:   "pack <manifest.json | ->",
		Short: "Build a binpazer file from a JSON manifest",
		Long: `Build a binpazer file from a JSON manifest.

"-" reads the manifest from stdin (file references then resolve relative to
the current directory). "--out -" writes the archive to stdout; the archive
is assembled in memory first so its bytes are identical to file output.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if out == "" {
				return fmt.Errorf("--out is required")
			}
			var (
				raw     []byte
				err     error
				baseDir string
			)
			if args[0] == "-" {
				raw, err = io.ReadAll(cmd.InOrStdin())
				baseDir = "."
			} else {
				raw, err = os.ReadFile(args[0])
				baseDir = filepath.Dir(args[0])
			}
			if err != nil {
				return err
			}
			var m Manifest
			if err := json.Unmarshal(raw, &m); err != nil {
				return fmt.Errorf("parsing manifest: %w", err)
			}
			if out == "-" {
				mf := &memFile{}
				if err := runPack(&m, baseDir, mf); err != nil {
					return err
				}
				if _, err := cmd.OutOrStdout().Write(mf.buf); err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "wrote %d bytes to stdout\n", len(mf.buf))
				return nil
			}
			f, err := os.Create(out)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := runPack(&m, baseDir, f); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "output file, or - for stdout (required)")
	rootCmd.AddCommand(cmd)
}
