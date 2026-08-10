package tgb_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/tgb"
)

// sentinelGraph builds a graph containing tango's empty-hash cycle sentinel:
// HashRecursively sets Hash = []byte{} (hex "") to break dependency cycles.
func sentinelGraph(emptyIDs ...int32) *tgb.Graph {
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
		h := widthTestHash(int(id))
		if empty[id] {
			h = "" // the sentinel
		}
		if id == 4 {
			// A legitimate(ish) all-zero hash, to pin down that it stays
			// distinct from the sentinel through a round-trip.
			h = "0000000000000000000000000000000000000000"
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

// TestEmptyHashSentinelRoundTrip: "" must decode back as "", not as a string
// of hex zeros, at every hash width.
func TestEmptyHashSentinelRoundTrip(t *testing.T) {
	for _, hb := range []int{8, 16, 20} {
		g := sentinelGraph(2)
		data := mustEncode(t, g, tgb.EncodeOptions{HashBytes: hb, BlockSize: 4})
		got := mustDecode(t, data)

		byLabel := map[string]string{}
		for _, tt := range got.Targets {
			byLabel[got.Metadata.TargetIDMapping[tt.ID]] = tt.Hash
		}
		if h := byLabel["//src/a:cyclic"]; h != "" {
			t.Errorf("hb=%d: sentinel decoded as %q, want \"\"", hb, h)
		}
		if h := byLabel["//src/b:zeros"]; h == "" {
			t.Errorf("hb=%d: all-zero hash decoded as sentinel \"\"", hb)
		}
		if h := byLabel["//src/a:lib"]; h == "" {
			t.Errorf("hb=%d: real hash decoded as sentinel", hb)
		}

		// Full lossless check at 20 bytes.
		if hb == 20 {
			if !reflect.DeepEqual(canonicalise(g), canonicalise(got)) {
				t.Errorf("hb=20: sentinel graph round-trip not lossless")
			}
		}
	}
}

// TestEncodeRejectsBadHashes: silent zero-padding was how a short or garbage
// hash used to corrupt the round-trip; now it must be a loud error.
func TestEncodeRejectsBadHashes(t *testing.T) {
	for _, bad := range []string{"zz", "abcd", "0102030405"} {
		g := sentinelGraph()
		g.Targets[0].Hash = bad
		var buf bytes.Buffer
		err := tgb.Encode(&buf, g, tgb.EncodeOptions{HashBytes: 8, BlockSize: 4})
		if err == nil {
			t.Errorf("hash %q: encode succeeded, want error", bad)
		}
	}
}
