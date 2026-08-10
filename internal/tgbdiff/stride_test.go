package tgbdiff_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"math/rand"
	"testing"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/tgb"
	"github.com/uber/tango/internal/tgbdiff"
)

// strideTestHash builds a deterministic 20-byte hex hash distinct per seed.
func strideTestHash(seed int) string {
	raw := make([]byte, 20)
	for i := range raw {
		raw[i] = byte(seed*31 + i*7 + 1)
	}
	return hex.EncodeToString(raw)
}

// encodeAt encodes g at the given hash stride and returns a Reader.
func encodeAt(t *testing.T, g *tgb.Graph, hashBytes int) *tgb.Reader {
	t.Helper()
	var buf bytes.Buffer
	if err := tgb.Encode(&buf, g, tgb.EncodeOptions{HashBytes: hashBytes, BlockSize: 16}); err != nil {
		t.Fatalf("tgb.Encode: %v", err)
	}
	r, err := tgb.NewReader(buf.Bytes())
	if err != nil {
		t.Fatalf("tgb.NewReader: %v", err)
	}
	return r
}

// TestHashWidthDiff flips one hash and checks the comparison detects it at
// every stride.
func TestHashWidthDiff(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for _, hb := range []int{8, 16, 20} {
		before := syntheticGraph(rng, 300, 0.3)

		// Deep-enough copy: fresh Targets slice, same metadata (unchanged).
		afterTargets := make([]entity.OptimizedTarget, len(before.Targets))
		copy(afterTargets, before.Targets)
		afterTargets[42].Hash = strideTestHash(99)
		after := &tgb.Graph{Targets: afterTargets, Metadata: before.Metadata}

		res, err := tgbdiff.Compare(context.Background(),
			encodeAt(t, before, hb), encodeAt(t, after, hb),
			tgbdiff.Options{MaxDistance: -1})
		if err != nil {
			t.Fatalf("hb=%d: Compare: %v", hb, err)
		}
		if len(res.Changed) == 0 {
			t.Errorf("hb=%d: hash flip not detected", hb)
		}
	}
}

// TestHashStrideMismatch checks that comparing blobs of different widths is
// rejected instead of silently misaligning phase 1.
func TestHashStrideMismatch(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	g := syntheticGraph(rng, 50, 0.3)

	_, err := tgbdiff.Compare(context.Background(),
		encodeAt(t, g, 8), encodeAt(t, g, 20),
		tgbdiff.Options{MaxDistance: -1})
	if err == nil {
		t.Fatal("comparing 8-byte blob against 20-byte blob succeeded; want stride-mismatch error")
	}
}

// diffSentinelGraph builds a graph containing tango's empty-hash cycle
// sentinel: HashRecursively sets Hash = []byte{} (hex "") to break cycles.
func diffSentinelGraph(emptyIDs ...int32) *tgb.Graph {
	empty := make(map[int32]bool, len(emptyIDs))
	for _, id := range emptyIDs {
		empty[id] = true
	}
	labels := map[int32]string{
		1: "//src/a:lib",
		2: "//src/a:cyclic",
		3: "//src/b:lib",
		4: "//src/b:zeros",
	}
	targets := make([]entity.OptimizedTarget, 0, len(labels))
	for id := int32(1); id <= 4; id++ {
		h := strideTestHash(int(id))
		if id == 4 {
			h = "0000000000000000000000000000000000000000"
		}
		if empty[id] {
			h = "" // the sentinel; wins over node 4's all-zero digest
		}
		targets = append(targets, entity.OptimizedTarget{
			ID: id, Hash: h, RuleType: 1,
		})
	}
	return &tgb.Graph{
		Targets: targets,
		Metadata: entity.Metadata{
			TargetIDMapping:             labels,
			RuleTypeMapping:             map[int32]string{1: "go_library"},
			TagMapping:                  map[int32]string{},
			AttributeNameMapping:        map[int32]string{},
			AttributeStringValueMapping: map[int32]string{},
		},
	}
}

// TestEmptyHashSentinelDiff: a sentinel appearing or disappearing must be
// reported as a change, identically by tgbdiff and the targetdiff oracle.
func TestEmptyHashSentinelDiff(t *testing.T) {
	before := diffSentinelGraph() // node 2 has a real hash
	after := diffSentinelGraph(2) // node 2 became the sentinel

	fast, err := tgbdiff.Compare(context.Background(),
		encodeAt(t, before, 20), encodeAt(t, after, 20),
		tgbdiff.Options{MaxDistance: -1})
	if err != nil {
		t.Fatalf("tgbdiff: %v", err)
	}
	ref := oracleCompare(t, before, after, nil, -1)
	if len(fast.Changed) == 0 {
		t.Error("sentinel transition not detected as a change")
	}
	if len(fast.Changed) != len(ref.Changed) {
		t.Fatalf("sentinel diff mismatch: tgbdiff %d changed, oracle %d changed",
			len(fast.Changed), len(ref.Changed))
	}
	for i := range fast.Changed {
		if fast.Changed[i] != ref.Changed[i] {
			t.Errorf("sentinel diff mismatch at %d:\n  tgbdiff: %+v\n  oracle:  %+v",
				i, fast.Changed[i], ref.Changed[i])
		}
	}
}

// TestSentinelVsZeroHashDiff pins the case rapid found: the sentinel ("") and
// the all-zero digest are byte-identical in the HASH column, distinguished
// only by the hash-empty bitset. A node flipping between them must be
// reported as changed — targetdiff compares hash strings, where "" != "00…0".
func TestSentinelVsZeroHashDiff(t *testing.T) {
	for name, tc := range map[string]struct{ before, after *tgb.Graph }{
		"sentinel-to-zeros": {diffSentinelGraph(2), diffSentinelGraph(2, 4)},
		"zeros-to-sentinel": {diffSentinelGraph(2, 4), diffSentinelGraph(2)},
	} {
		fast, err := tgbdiff.Compare(context.Background(),
			encodeAt(t, tc.before, 20), encodeAt(t, tc.after, 20),
			tgbdiff.Options{MaxDistance: -1})
		if err != nil {
			t.Fatalf("%s: tgbdiff: %v", name, err)
		}
		ref := oracleCompare(t, tc.before, tc.after, nil, -1)
		if len(fast.Changed) != len(ref.Changed) {
			t.Fatalf("%s: tgbdiff %d changed, oracle %d changed",
				name, len(fast.Changed), len(ref.Changed))
		}
		if len(fast.Changed) == 0 {
			t.Errorf("%s: sentinel/zero-hash flip not detected", name)
		}
		for i := range fast.Changed {
			if fast.Changed[i] != ref.Changed[i] {
				t.Errorf("%s: mismatch at %d:\n  tgbdiff: %+v\n  oracle:  %+v",
					name, i, fast.Changed[i], ref.Changed[i])
			}
		}
	}
}
