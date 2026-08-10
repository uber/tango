package tgb_test

import (
	"encoding/hex"
	"math/rand"
	"reflect"
	"testing"

	"github.com/uber/tango/internal/tgb"
)

// TestHashWidths round-trips the same graph at every supported hash stride.
//
// The 20-byte case is the important one: tango's hashes are full SHA-1s served
// to clients as hex, so at stride 20 the round-trip must be lossless — no
// TruncateHashes on the expectation side. The 8- and 16-byte cases remain
// lossy by design and are compared against the truncated graph.
func TestHashWidths(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	graphs := []struct {
		name string
		mk   func() *tgb.Graph
	}{
		{"tiny", buildTinyGraph},
		{"random200", func() *tgb.Graph { return randomGraph(rng, 200) }},
	}

	for _, gc := range graphs {
		for _, hb := range []int{8, 16, 20} {
			g := gc.mk()
			opts := tgb.EncodeOptions{HashBytes: hb, BlockSize: 16}
			data := mustEncode(t, g, opts)

			r, err := tgb.NewReader(data)
			if err != nil {
				t.Fatalf("%s/hb=%d: NewReader: %v", gc.name, hb, err)
			}
			if got := r.HashBytes(); got != hb {
				t.Fatalf("%s/hb=%d: HashBytes() = %d", gc.name, hb, got)
			}
			if hashes := r.Hashes(); len(hashes) != r.NodeCount()*hb {
				t.Fatalf("%s/hb=%d: HASH column %d bytes, want %d",
					gc.name, hb, len(hashes), r.NodeCount()*hb)
			}

			got := canonicalise(mustDecode(t, data))
			var want *tgb.Graph
			if hb == 20 {
				// Full SHA-1 width: truncation must be the identity, and the
				// round-trip must be lossless against the original graph.
				want = canonicalise(g)
			} else {
				want = canonicalise(tgb.TruncateHashes(g, hb))
			}
			if !reflect.DeepEqual(want, got) {
				t.Errorf("%s/hb=%d: round-trip mismatch", gc.name, hb)
			}
		}
	}
}

// widthTestHash builds a deterministic 20-byte hex hash distinct per seed.
func widthTestHash(seed int) string {
	raw := make([]byte, 20)
	for i := range raw {
		raw[i] = byte(seed*31 + i*7 + 1)
	}
	return hex.EncodeToString(raw)
}
