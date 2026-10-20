package binpazer

import (
	"bytes"
	"testing"
)

// FuzzReader hammers the full read surface: NewReader (header + block type
// table), the Next/ReadPayload/SkipPayload walk, and Find/At. Property: no
// panics and no unbounded allocations -- every length/count field is
// bounds-checked against the input size before it sizes an allocation.
// Hostile inputs fail with typed errors (the companion tests in
// hardening_test.go assert the exact errors for each crafted seed).
func FuzzReader(f *testing.F) {
	sample := sampleBytes(f)
	f.Add(sample)
	f.Add([]byte("binpazer")) // magic only: truncated header
	f.Add(hugePayloadSample(f))
	f.Add(hugeBTTCountSample(f))
	f.Add(hugeNameLenSample(f))
	f.Add(corruptCRCSample(f)) // CRC mismatch path
	for _, n := range []int{4, 56, 60, 100, 150, 224, 279} {
		f.Add(sample[:n]) // truncated prefixes
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		br, err := NewReader(bytes.NewReader(data))
		if err != nil {
			return
		}
		for i := 0; ; i++ {
			b, err := br.Next()
			if err != nil {
				break
			}
			if i%2 == 0 {
				if _, err := br.ReadPayload(b); err != nil {
					break
				}
			} else if err := br.SkipPayload(b); err != nil {
				break
			}
		}
		offs, err := br.Find(1)
		if err != nil {
			return
		}
		for _, o := range offs {
			b, err := br.At(o)
			if err != nil {
				return
			}
			if _, err := br.ReadPayload(b); err != nil {
				return
			}
		}
	})
}
