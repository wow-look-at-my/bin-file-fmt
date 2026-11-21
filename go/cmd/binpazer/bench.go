// bench.go holds the hidden bench command: fixed workloads whose one-line output a cross-language driver compares.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/pprof"
	"strconv"

	"github.com/spf13/cobra"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

// benchTypeID is the block type every bench file carries.
const benchTypeID = 1

// benchCodecs maps a bench codec name to its registry id; none writes a plain block.
var benchCodecs = map[string]uint16{
	"none":    binpazer.CodecInvalid,
	"stored":  binpazer.CodecStored,
	"deflate": binpazer.CodecDeflate,
	"zstd":    binpazer.CodecZstd,
	"lz4":     binpazer.CodecLZ4,
}

// benchSum is the 32-bit FNV-1a checksum over the length and sample bytes of each block.
type benchSum uint32

func newBenchSum() benchSum { return 2166136261 }

func (h *benchSum) mix(b byte) { *h = (*h ^ benchSum(b)) * 16777619 }

func (h *benchSum) mixLen(n uint64) {
	for i := 0; i < 4; i++ {
		h.mix(byte(uint32(n) >> (8 * i)))
	}
}

// block mixes one block from its length and its first, middle and last bytes.
func (h *benchSum) block(n uint64, first, mid, last byte) {
	h.mixLen(n)
	if n > 0 {
		h.mix(first)
		h.mix(mid)
		h.mix(last)
	}
}

func (h *benchSum) payload(p []byte) {
	n := len(p)
	if n == 0 {
		h.block(0, 0, 0, 0)
		return
	}
	h.block(uint64(n), p[0], p[n>>1], p[n-1])
}

// benchResult is the line a successful bench run prints.
type benchResult struct {
	op     string
	blocks int
	bytes  uint64
	sum    benchSum
}

// benchLineJSON fixes the key order of the result line.
type benchLineJSON struct {
	Op       string `json:"op"`
	Blocks   int    `json:"blocks"`
	Bytes    uint64 `json:"bytes"`
	Checksum string `json:"checksum"`
}

func printBench(cmd *cobra.Command, r benchResult) error {
	// Marshal cannot fail: every field is a string or an integer.
	line, _ := json.Marshal(benchLineJSON{r.op, r.blocks, r.bytes, fmt.Sprintf("%08x", uint32(r.sum))})
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\n", line)
	return err
}

func parseCount(s, what string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("bench: %s must be a non-negative integer, got %q", what, s)
	}
	return n, nil
}

func benchWrite(out, payloadPath, blocksArg, codec string) (benchResult, error) {
	blocks, err := parseCount(blocksArg, "block count")
	if err != nil {
		return benchResult{}, err
	}
	codecID, ok := benchCodecs[codec]
	if !ok {
		return benchResult{}, fmt.Errorf("bench: unknown codec %q (want none, stored, deflate, zstd or lz4)", codec)
	}
	payload, err := os.ReadFile(payloadPath)
	if err != nil {
		return benchResult{}, err
	}
	f, err := os.Create(out)
	if err != nil {
		return benchResult{}, err
	}
	defer f.Close()
	writerGUID := binpazer.GUID{0xB0}
	defs := []binpazer.TypeDef{{TypeID: benchTypeID, GUID: binpazer.GUID{0x11}, Name: "Payload"}}
	// NewWriter always returns a Writer, and a Writer error stays set, so Finish reports the first failure.
	bw, _ := binpazer.NewWriter(f, writerGUID, "bench", defs)
	for i := 0; i < blocks; i++ {
		if codec == "none" {
			err = bw.Block(benchTypeID, binpazer.FlagHasCRC, payload)
		} else {
			err = bw.CompressedBlock(benchTypeID, binpazer.FlagHasCRC, codecID, payload)
		}
		if err != nil {
			break
		}
	}
	if err = bw.Finish(); err == nil {
		err = f.Close()
	}
	if err != nil {
		return benchResult{}, err
	}
	return benchResult{op: "write", blocks: blocks, bytes: uint64(blocks) * uint64(len(payload))}, nil
}

// openBench opens a seekable reader on path; the caller closes the file.
func openBench(path string) (*binpazer.Reader, *os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	br, err := binpazer.NewReader(f)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return br, f, nil
}

// walkBench calls fn for each type-1 block and skips every other block.
func walkBench(path string, fn func(*binpazer.Reader, *binpazer.Block) error) error {
	br, f, err := openBench(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		b, err := br.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if b.TypeID == benchTypeID {
			err = fn(br, b)
		} else {
			err = br.SkipPayload(b)
		}
		if err != nil {
			return err
		}
	}
}

func benchRead(path string) (benchResult, error) {
	r := benchResult{op: "read", sum: newBenchSum()}
	err := walkBench(path, func(br *binpazer.Reader, b *binpazer.Block) error {
		p, err := br.ReadDecompressedPayload(b)
		if err != nil {
			return err
		}
		r.blocks++
		r.bytes += uint64(len(p))
		r.sum.payload(p)
		return nil
	})
	return r, err
}

// sampleWriter counts streamed bytes and keeps the first, middle and last of them.
type sampleWriter struct {
	n, mid          uint64
	first, at, last byte
}

func (s *sampleWriter) Write(p []byte) (int, error) {
	for i, c := range p {
		pos := s.n + uint64(i)
		if pos == 0 {
			s.first = c
		}
		if pos == s.mid {
			s.at = c
		}
	}
	if len(p) > 0 {
		s.last = p[len(p)-1]
	}
	s.n += uint64(len(p))
	return len(p), nil
}

func benchStream(path string) (benchResult, error) {
	r := benchResult{op: "stream", sum: newBenchSum()}
	err := walkBench(path, func(br *binpazer.Reader, b *binpazer.Block) error {
		s := &sampleWriter{mid: b.PayloadLen >> 1}
		if _, err := br.WritePayloadTo(b, s); err != nil {
			return err
		}
		r.blocks++
		r.bytes += s.n
		r.sum.block(s.n, s.first, s.at, s.last)
		return nil
	})
	return r, err
}

func benchFind(path, lookupsArg string) (benchResult, error) {
	lookups, err := parseCount(lookupsArg, "lookup count")
	if err != nil {
		return benchResult{}, err
	}
	br, f, err := openBench(path)
	if err != nil {
		return benchResult{}, err
	}
	defer f.Close()
	offs, err := br.Find(benchTypeID)
	if err != nil {
		return benchResult{}, err
	}
	if len(offs) == 0 {
		return benchResult{}, errors.New("bench: the input has no type-1 blocks")
	}
	r := benchResult{op: "find", blocks: lookups, sum: newBenchSum()}
	m := uint32(len(offs))
	for k := 0; k < lookups; k++ {
		b, err := br.At(offs[(uint32(k)*2654435761)%m])
		if err != nil {
			return benchResult{}, err
		}
		p, err := br.ReadDecompressedPayload(b)
		if err != nil {
			return benchResult{}, err
		}
		r.bytes += uint64(len(p))
		r.sum.payload(p)
	}
	return r, nil
}

// withProfile runs fn under a CPU profile into path when path is set, and stops the profile on every exit.
func withProfile(path string, fn func() (benchResult, error)) (benchResult, error) {
	if path == "" {
		return fn()
	}
	f, err := os.Create(path)
	if err != nil {
		return benchResult{}, err
	}
	defer f.Close()
	if err := pprof.StartCPUProfile(f); err != nil {
		return benchResult{}, err
	}
	r, err := fn()
	pprof.StopCPUProfile()
	return r, err
}

func init() {
	var cpuProfile string
	bench := &cobra.Command{
		Use:    "bench",
		Short:  "Run a fixed workload and print one result line (for the cross-language benchmark driver)",
		Hidden: true,
	}
	bench.PersistentFlags().StringVar(&cpuProfile, "cpuprofile", "", "write a CPU profile to this file")
	sub := func(use string, nargs int, fn func(args []string) (benchResult, error)) {
		bench.AddCommand(&cobra.Command{
			Use:  use,
			Args: cobra.ExactArgs(nargs),
			RunE: func(cmd *cobra.Command, args []string) error {
				r, err := withProfile(cpuProfile, func() (benchResult, error) { return fn(args) })
				if err != nil {
					return err
				}
				return printBench(cmd, r)
			},
		})
	}
	sub("write <out-file> <payload-file> <blocks> <codec>", 4, func(a []string) (benchResult, error) {
		return benchWrite(a[0], a[1], a[2], a[3])
	})
	sub("read <in-file>", 1, func(a []string) (benchResult, error) { return benchRead(a[0]) })
	sub("stream <in-file>", 1, func(a []string) (benchResult, error) { return benchStream(a[0]) })
	sub("find <in-file> <lookups>", 2, func(a []string) (benchResult, error) { return benchFind(a[0], a[1]) })
	rootCmd.AddCommand(bench)
}
