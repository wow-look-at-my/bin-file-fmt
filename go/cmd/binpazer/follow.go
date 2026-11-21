// follow.go holds the follow command, which prints blocks of an appendable file as they are committed.

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// followLine is one line of follow output: a committed block, with its payload decoded.
type followLine struct {
	Index      int      `json:"index"`
	TypeID     uint16   `json:"type_id"`
	TypeName   string   `json:"type_name,omitempty"`
	Flags      []string `json:"flags,omitempty"`
	Offset     uint64   `json:"offset"`
	PayloadLen uint64   `json:"payload_len"`
	Codec      string   `json:"codec,omitempty"`
	PayloadB64 *string  `json:"payload_base64,omitempty"`
}

// follower holds the options of one follow run.
type follower struct {
	wait      bool
	interval  time.Duration
	noPayload bool
	maxAlloc  int64 // cap on one decoded payload
	out       io.Writer
	errOut    io.Writer
}

// payload moves the reader past the block and returns its decoded payload and its codec name.
// The payload is nil when the run leaves payloads out.
func (f *follower) payload(br *binpazer.Reader, b *binpazer.Block) ([]byte, string, error) {
	if b.Flags&binpazer.FlagCompressed == 0 {
		if f.noPayload {
			return nil, "", br.SkipPayload(b)
		}
		p, err := br.ReadPayload(b)
		return p, "", err
	}
	c, r, err := br.DecompressedPayload(b)
	if err != nil {
		return nil, "", err
	}
	var p []byte
	if !f.noPayload {
		// The envelope's length is untrusted, so it is held to the allocation cap before the decode.
		if c.UncompressedLen > uint64(f.maxAlloc) {
			return nil, "", fmt.Errorf("block at offset %d decodes to %d bytes, over the %d-byte cap: %w",
				b.Offset(), c.UncompressedLen, f.maxAlloc, binpazer.ErrCapacityExceeded)
		}
		if p, err = io.ReadAll(r); err != nil {
			return nil, "", err
		}
	}
	return p, codecName(c.CodecID), br.SkipPayload(b)
}

// emit prints one committed block as a JSON line and moves the reader past it.
func (f *follower) emit(br *binpazer.Reader, tt *typeTable, b *binpazer.Block, n int) error {
	line := followLine{
		Index: n, TypeID: b.TypeID, TypeName: tt.name(b.TypeID),
		Flags: displayFlagNames(b.Flags), Offset: b.Offset(), PayloadLen: b.PayloadLen,
	}
	p, codec, err := f.payload(br, b)
	if err != nil {
		return err
	}
	line.Codec = codec
	if !f.noPayload {
		s := base64.StdEncoding.EncodeToString(p)
		line.PayloadB64 = &s
	}
	raw, err := json.Marshal(line)
	if err != nil {
		return err
	}
	_, err = f.out.Write(append(raw, '\n'))
	return err
}

// run follows a seekable appendable file until its End block, or until nothing more is committed when wait is off.
func (f *follower) run(br *binpazer.Reader) error {
	tt := newTypeTable(br)
	warned := false
	for n := 0; ; {
		b, err := br.NextCommitted()
		switch {
		case err == io.EOF:
			return nil
		case errors.Is(err, binpazer.ErrTorn):
			if !f.wait {
				return err
			}
			if !warned {
				fmt.Fprintf(f.errOut, "warning: %v; waiting for a writer to resume the file\n", err)
				warned = true
			}
			time.Sleep(f.interval)
		case errors.Is(err, binpazer.ErrPending):
			if !f.wait {
				return nil
			}
			time.Sleep(f.interval)
		case err != nil:
			return err
		default:
			warned = false
			if err := f.emit(br, tt, b, n); err != nil {
				return err
			}
			n++
		}
	}
}

// runStream follows a pipe: the blocks arrive in order, and each read waits for its bytes.
func (f *follower) runStream(br *binpazer.Reader) error {
	if !br.Appendable() {
		return binpazer.ErrNotAppendable
	}
	tt := newTypeTable(br)
	for n := 0; ; n++ {
		b, err := br.Next()
		if err == io.EOF {
			if !br.Ended() {
				return errors.New("input stopped before the End block: the writer did not close the stream")
			}
			return nil
		}
		if err != nil {
			return err
		}
		if err := f.emit(br, tt, b, n); err != nil {
			return err
		}
	}
}

// openFollowed opens the file to follow. With wait on, it retries while the writer has not finished the header.
func (f *follower) openFollowed(cmd *cobra.Command, path string, maxAlloc int64) (*binpazer.Reader, io.Closer, error) {
	for {
		br, c, err := openReader(cmd, path, maxAlloc)
		if f.wait && path != "-" && errors.Is(err, binpazer.ErrTruncated) {
			time.Sleep(f.interval)
			continue
		}
		return br, c, err
	}
}

func init() {
	var (
		noWait, noPayload bool
		interval          time.Duration
		maxAlloc          *int64
	)
	cmd := &cobra.Command{
		Use:   "follow <file.binpazer | ->",
		Short: "Print the blocks of an appendable file as a writer commits them",
		Long: `Print each committed block of an appendable file as one JSON line, and keep
waiting for more while a writer appends. A block is printed only after it is
committed: all its bytes are in the file and its CRC seal verifies, so a block
that is still being written never appears.

follow exits 0 when the writer ends the file. --no-wait prints what is
committed now and exits. The payload is decoded when the block is compressed,
and printed as base64; --no-payload leaves it out.

A torn block (all bytes present, seal incorrect) is the tail of an
interrupted writer. follow warns and keeps waiting, because "binpazer append"
repairs it; with --no-wait it is an error.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if interval <= 0 {
				return fmt.Errorf("--interval must be greater than zero")
			}
			f := &follower{
				wait: !noWait, interval: interval, noPayload: noPayload, maxAlloc: *maxAlloc,
				out: cmd.OutOrStdout(), errOut: cmd.ErrOrStderr(),
			}
			if f.maxAlloc <= 0 {
				f.maxAlloc = binpazer.DefaultMaxAlloc
			}
			br, c, err := f.openFollowed(cmd, args[0], *maxAlloc)
			if err != nil {
				return err
			}
			if c != nil {
				defer c.Close()
			}
			if br.InputSize() < 0 {
				return f.runStream(br)
			}
			return f.run(br)
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "print the blocks committed now and exit, instead of waiting for the End block")
	cmd.Flags().BoolVar(&noPayload, "no-payload", false, "leave payload_base64 out of each line")
	cmd.Flags().DurationVar(&interval, "interval", 200*time.Millisecond, "how long to wait before looking for a new block")
	maxAlloc = maxAllocFlag(cmd)
	rootCmd.AddCommand(cmd)
}
