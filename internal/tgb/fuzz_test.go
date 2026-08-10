package tgb_test

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/uber/tango/internal/tgb"
)

// fuzzSeedBlobs builds a few valid blobs to seed the corpus: tiny graph,
// random graph, sentinel graph, at a couple of widths and block sizes.
func fuzzSeedBlobs(tb testing.TB) [][]byte {
	rng := rand.New(rand.NewSource(1))
	var blobs [][]byte
	encode := func(g *tgb.Graph, hb, bs int) {
		var buf bytes.Buffer
		if err := tgb.Encode(&buf, g, tgb.EncodeOptions{HashBytes: hb, BlockSize: bs}); err != nil {
			tb.Fatalf("seed encode: %v", err)
		}
		blobs = append(blobs, buf.Bytes())
	}
	encode(buildTinyGraph(), 8, 4)
	encode(buildTinyGraph(), 20, 16)
	encode(randomGraph(rng, 300), 8, 16)
	encode(sentinelGraph(2), 20, 4)
	return blobs
}

// FuzzNewReader feeds arbitrary bytes to NewReader and, when it accepts,
// exercises every lazy accessor. The invariant is simple: hostile bytes may
// produce errors or zero values, never a panic and never an oversized
// allocation (the harness itself OOMing counts as a failure).
func FuzzNewReader(f *testing.F) {
	for _, b := range fuzzSeedBlobs(f) {
		f.Add(b)
		// Truncations and bit flips of valid blobs reach deeper than random
		// bytes, which mostly die at the magic check.
		for _, cut := range []int{1, 8, len(b) / 2, len(b) - 1} {
			if cut > 0 && cut < len(b) {
				f.Add(b[:cut])
			}
		}
		for _, off := range []int{4, 40, len(b) / 2, len(b) - 4} {
			if off >= 0 && off < len(b) {
				mut := append([]byte(nil), b...)
				mut[off] ^= 0x40
				f.Add(mut)
			}
		}
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := tgb.NewReader(data)
		if err != nil {
			return
		}
		n := r.NodeCount()
		// validateColumns ties nodeCount to real column bytes; anything huge
		// escaping it means the bound is broken — fail before the accessors
		// below try to allocate off it.
		if n > 1<<26 {
			t.Fatalf("hostile blob passed validation with nodeCount=%d from %d bytes", n, len(data))
		}

		_ = r.HashBytes()
		_ = r.Hashes()
		_ = r.BlockCount()
		_ = r.BlockDigests()
		for i := 0; i < r.BlockCount() && i < 64; i++ {
			_ = r.BlockStart(i)
		}
		probe := []int{0, 1, n / 2, n - 1}
		var depBuf, tagBuf []int32
		for _, i := range probe {
			if i < 0 || i >= n {
				continue
			}
			_ = r.Label(i)
			_ = r.RuleType(i)
			_ = r.Root(i)
			_ = r.External(i)
			_ = r.HashEmpty(i)
			depBuf = r.Deps(i, depBuf[:0])
			tagBuf = r.Tags(i, tagBuf[:0])
			_ = r.Attrs(i)
		}
		_, _ = r.RuleTypeIDs()
		_, _, _ = r.DepsCSR()
	})
}

// FuzzDecode drives the full materialisation path, which walks every varint
// stream to the end and so reaches offsets the lazy accessors may not.
func FuzzDecode(f *testing.F) {
	for _, b := range fuzzSeedBlobs(f) {
		f.Add(b)
		if len(b) > 100 {
			mut := append([]byte(nil), b...)
			// Corrupt a byte inside the column region (past the header).
			mut[64+7] ^= 0xff
			f.Add(mut)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		g, err := tgb.Decode(data)
		if err != nil {
			return
		}
		if g == nil {
			t.Fatal("Decode returned nil graph and nil error")
		}
	})
}
