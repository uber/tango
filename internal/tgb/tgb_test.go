package tgb_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/tgb"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func mustEncode(t *testing.T, g *tgb.Graph, opts tgb.EncodeOptions) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := tgb.Encode(&buf, g, opts); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return buf.Bytes()
}

func mustDecode(t *testing.T, data []byte) *tgb.Graph {
	t.Helper()
	g, err := tgb.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return g
}

// canonicalise normalises a tgb.Graph so that reflect.DeepEqual gives a
// meaningful structural comparison:
//
//   - Targets are sorted by label (derived from TargetIDMapping).
//   - DirectDependencies, Tags are sorted ascending.
//   - Attributes map is preserved as-is (map comparison is already order-independent).
//
// Why: The encoder reorders nodes into (pkg,name) sort order and assigns new
// sequential IDs starting from 0. The decoder reproduces that ordering, so
// after canonicalisation both the original (truncated) graph and the decoded
// graph must have the same label sequence and the same structure.
func canonicalise(g *tgb.Graph) *tgb.Graph {
	// Step 1: build label→target map from the original (using original IDs).
	type entry struct {
		label  string
		target entity.OptimizedTarget
	}
	entries := make([]entry, len(g.Targets))
	for i, t := range g.Targets {
		label := g.Metadata.TargetIDMapping[t.ID]
		entries[i] = entry{label: label, target: t}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].label < entries[j].label
	})

	// Step 2: assign new sequential IDs in sorted order.
	oldToNew := make(map[int32]int32, len(entries))
	for newID, e := range entries {
		oldToNew[e.target.ID] = int32(newID)
	}

	// Step 3: rebuild targets with new IDs and sorted dep/tag slices.
	newTargets := make([]entity.OptimizedTarget, len(entries))
	newIDMap := make(map[int32]string, len(entries))
	for newID, e := range entries {
		t := e.target

		// remap deps
		deps := make([]int32, len(t.DirectDependencies))
		for j, d := range t.DirectDependencies {
			deps[j] = oldToNew[d]
		}
		sort.Slice(deps, func(a, b int) bool { return deps[a] < deps[b] })

		// sort tags
		tags := make([]int32, len(t.Tags))
		copy(tags, t.Tags)
		sort.Slice(tags, func(a, b int) bool { return tags[a] < tags[b] })

		newTargets[newID] = entity.OptimizedTarget{
			ID:                 int32(newID),
			Hash:               t.Hash,
			DirectDependencies: deps,
			RuleType:           t.RuleType,
			Tags:               tags,
			Root:               t.Root,
			External:           t.External,
			Attributes:         t.Attributes,
		}
		newIDMap[int32(newID)] = e.label
	}

	// Step 4: build normalised metadata.
	// RuleType IDs, Tag IDs, AttrName IDs, AttrValue IDs are left as-is
	// because the encoder builds its own dicts sorted alphabetically, and
	// Decode reproduces those same sorted dicts. So RuleType int32 in the
	// decoded graph is the index into the sorted ruleTypeDict, which equals
	// the index into the sorted ruleTypeDict of the re-encoded original.
	// We just need the string values to match.

	// Build sorted rule-type dict from g.Metadata.
	rtStrings := sortedValues(g.Metadata.RuleTypeMapping)
	rtMap := make(map[int32]string, len(rtStrings))
	rtRevMap := make(map[string]int32, len(rtStrings))
	for i, s := range rtStrings {
		rtMap[int32(i)] = s
		rtRevMap[s] = int32(i)
	}

	tagStrings := sortedValues(g.Metadata.TagMapping)
	tagMap := make(map[int32]string, len(tagStrings))
	tagRevMap := make(map[string]int32, len(tagStrings))
	for i, s := range tagStrings {
		tagMap[int32(i)] = s
		tagRevMap[s] = int32(i)
	}

	anStrings := sortedValues(g.Metadata.AttributeNameMapping)
	anMap := make(map[int32]string, len(anStrings))
	anRevMap := make(map[string]int32, len(anStrings))
	for i, s := range anStrings {
		anMap[int32(i)] = s
		anRevMap[s] = int32(i)
	}

	avStrings := sortedValues(g.Metadata.AttributeStringValueMapping)
	avMap := make(map[int32]string, len(avStrings))
	avRevMap := make(map[string]int32, len(avStrings))
	for i, s := range avStrings {
		avMap[int32(i)] = s
		avRevMap[s] = int32(i)
	}

	// Remap RuleType, Tags, Attrs in targets using the sorted dicts.
	for i := range newTargets {
		t := &newTargets[i]
		// RuleType: old corpus int32 → string → new sorted dict index
		rtOldStr := g.Metadata.RuleTypeMapping[t.RuleType]
		t.RuleType = rtRevMap[rtOldStr]

		// Tags: old corpus int32 → string → new sorted index
		for j := range t.Tags {
			tagStr := g.Metadata.TagMapping[t.Tags[j]]
			t.Tags[j] = tagRevMap[tagStr]
		}
		sort.Slice(t.Tags, func(a, b int) bool { return t.Tags[a] < t.Tags[b] })

		// Attrs: old name/val corpus int32 → string → new sorted index
		newAttrs := make(map[int32]int32, len(t.Attributes))
		for an, av := range t.Attributes {
			anStr := g.Metadata.AttributeNameMapping[an]
			avStr := g.Metadata.AttributeStringValueMapping[av]
			newAttrs[anRevMap[anStr]] = avRevMap[avStr]
		}
		t.Attributes = newAttrs
	}

	return &tgb.Graph{
		Targets: newTargets,
		Metadata: entity.Metadata{
			TargetIDMapping:             newIDMap,
			RuleTypeMapping:             rtMap,
			TagMapping:                  tagMap,
			AttributeNameMapping:        anMap,
			AttributeStringValueMapping: avMap,
			AllTargetsFileHashes:        g.Metadata.AllTargetsFileHashes,
		},
	}
}

func sortedValues(m map[int32]string) []string {
	seen := make(map[string]struct{}, len(m))
	for _, v := range m {
		seen[v] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// ─── hand-built tiny graph ────────────────────────────────────────────────────

// buildTinyGraph constructs a hand-crafted graph with 15 nodes covering
// diverse cases: source file, no-dep node, many-dep node, empty attrs,
// non-empty attrs, root/external flags.
func buildTinyGraph() *tgb.Graph {
	// Rule types
	rtMap := map[int32]string{
		0: "source file",
		1: "go_library",
		2: "go_binary",
		3: "go_test",
	}
	// Tags
	tagMap := map[int32]string{
		0: "manual",
		1: "no-remote",
	}
	// Attr names / values
	anMap := map[int32]string{
		0: "visibility",
		1: "deprecated",
	}
	avMap := map[int32]string{
		0: "//visibility:public",
		1: "true",
		2: "false",
	}

	makeHash := func(seed int) string {
		raw := make([]byte, 20)
		for i := range raw {
			raw[i] = byte(seed*17 + i)
		}
		return hex.EncodeToString(raw)
	}

	targets := []entity.OptimizedTarget{
		// 0: source file, no deps, no attrs, root
		{ID: 10, Hash: makeHash(0), DirectDependencies: nil, RuleType: 0, Tags: nil, Root: true, External: false, Attributes: map[int32]int32{}},
		// 1: source file, no deps
		{ID: 11, Hash: makeHash(1), DirectDependencies: nil, RuleType: 0, Tags: nil, Root: false, External: false, Attributes: map[int32]int32{}},
		// 2: go_library with deps on 0,1
		{ID: 12, Hash: makeHash(2), DirectDependencies: []int32{10, 11}, RuleType: 1, Tags: []int32{0}, Root: false, External: false, Attributes: map[int32]int32{0: 0}},
		// 3: go_library with many deps (0..9 below)
		{ID: 13, Hash: makeHash(3), DirectDependencies: []int32{10, 11, 14, 15, 16, 17, 18, 19, 20, 21}, RuleType: 1, Tags: []int32{0, 1}, Root: false, External: true, Attributes: map[int32]int32{0: 0, 1: 1}},
		// 4: go_binary depending on 2,3
		{ID: 14, Hash: makeHash(4), DirectDependencies: []int32{12, 13}, RuleType: 2, Tags: nil, Root: true, External: false, Attributes: map[int32]int32{}},
		// 5..13: nine more nodes as go_test / go_library
		{ID: 15, Hash: makeHash(5), DirectDependencies: nil, RuleType: 3, Tags: nil, Root: false, External: false, Attributes: map[int32]int32{}},
		{ID: 16, Hash: makeHash(6), DirectDependencies: []int32{12}, RuleType: 3, Tags: nil, Root: false, External: false, Attributes: map[int32]int32{1: 2}},
		{ID: 17, Hash: makeHash(7), DirectDependencies: nil, RuleType: 1, Tags: nil, Root: false, External: true, Attributes: map[int32]int32{}},
		{ID: 18, Hash: makeHash(8), DirectDependencies: []int32{17}, RuleType: 1, Tags: []int32{1}, Root: false, External: false, Attributes: map[int32]int32{}},
		{ID: 19, Hash: makeHash(9), DirectDependencies: nil, RuleType: 0, Tags: nil, Root: false, External: false, Attributes: map[int32]int32{}},
		{ID: 20, Hash: makeHash(10), DirectDependencies: nil, RuleType: 0, Tags: nil, Root: false, External: false, Attributes: map[int32]int32{}},
		{ID: 21, Hash: makeHash(11), DirectDependencies: nil, RuleType: 0, Tags: nil, Root: false, External: false, Attributes: map[int32]int32{}},
		{ID: 22, Hash: makeHash(12), DirectDependencies: []int32{19, 20, 21}, RuleType: 1, Tags: nil, Root: false, External: false, Attributes: map[int32]int32{}},
		{ID: 23, Hash: makeHash(13), DirectDependencies: []int32{22}, RuleType: 2, Tags: nil, Root: false, External: false, Attributes: map[int32]int32{}},
		{ID: 24, Hash: makeHash(14), DirectDependencies: nil, RuleType: 3, Tags: []int32{0}, Root: false, External: false, Attributes: map[int32]int32{0: 0}},
	}

	idMap := map[int32]string{
		10: "//src/foo/bar:bar.go",
		11: "//src/foo/bar:baz.go",
		12: "//src/foo/bar:bar",
		13: "//src/foo/bar:all",
		14: "//src/foo/bar:cmd",
		15: "//src/foo/bar:bar_test",
		16: "//src/foo/qux:qux_test",
		17: "//third_party/go/somelib:somelib",
		18: "//src/foo/qux:qux",
		19: "//src/foo/qux:a.go",
		20: "//src/foo/qux:b.go",
		21: "//src/foo/qux:c.go",
		22: "//src/foo/qux:lib",
		23: "//src/foo/qux:bin",
		24: "//src/foo/qux:lib_test",
	}

	return &tgb.Graph{
		Targets: targets,
		Metadata: entity.Metadata{
			TargetIDMapping:             idMap,
			RuleTypeMapping:             rtMap,
			TagMapping:                  tagMap,
			AttributeNameMapping:        anMap,
			AttributeStringValueMapping: avMap,
		},
	}
}

// ─── Test: tiny graph round-trip ─────────────────────────────────────────────

func TestTinyGraphRoundTrip(t *testing.T) {
	g := buildTinyGraph()
	opts := tgb.EncodeOptions{HashBytes: 8, BlockSize: 4}
	data := mustEncode(t, g, opts)

	t.Logf("encoded size: %d bytes", len(data))

	// Decode and compare structurally.
	got := mustDecode(t, data)

	want := canonicalise(tgb.TruncateHashes(g, 8))
	got2 := canonicalise(got)

	if !reflect.DeepEqual(want, got2) {
		// Detailed diff.
		if len(want.Targets) != len(got2.Targets) {
			t.Fatalf("target count: want %d, got %d", len(want.Targets), len(got2.Targets))
		}
		for i := range want.Targets {
			if !reflect.DeepEqual(want.Targets[i], got2.Targets[i]) {
				t.Errorf("target %d mismatch:\n  want %+v\n  got  %+v",
					i, want.Targets[i], got2.Targets[i])
			}
		}
		if !reflect.DeepEqual(want.Metadata, got2.Metadata) {
			t.Errorf("metadata mismatch:\n  want %+v\n  got  %+v", want.Metadata, got2.Metadata)
		}
		t.FailNow()
	}
}

func TestAllTargetsFileHashesRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("present", func(t *testing.T) {
		t.Parallel()
		g := buildTinyGraph()
		g.Metadata.AllTargetsFileHashes = map[string]string{
			".bazelrc":         "abc123",
			"tools/bazel":      "def456",
			"rules/go/sdk.bzl": "789ghi",
		}

		data := mustEncode(t, g, tgb.EncodeOptions{HashBytes: 8, BlockSize: 4})
		got := mustDecode(t, data)

		assert.Equal(t, g.Metadata.AllTargetsFileHashes, got.Metadata.AllTargetsFileHashes)

		r, err := tgb.NewReader(data)
		require.NoError(t, err)
		atfh, err := r.AllTargetsFileHashes()
		require.NoError(t, err)
		assert.Equal(t, g.Metadata.AllTargetsFileHashes, atfh)
	})

	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		g := buildTinyGraph()

		data := mustEncode(t, g, tgb.EncodeOptions{HashBytes: 8, BlockSize: 4})
		got := mustDecode(t, data)

		assert.Nil(t, got.Metadata.AllTargetsFileHashes)

		r, err := tgb.NewReader(data)
		require.NoError(t, err)
		atfh, err := r.AllTargetsFileHashes()
		require.NoError(t, err)
		assert.Nil(t, atfh)
	})

	t.Run("empty map not encoded", func(t *testing.T) {
		t.Parallel()
		g := buildTinyGraph()
		g.Metadata.AllTargetsFileHashes = map[string]string{}

		data := mustEncode(t, g, tgb.EncodeOptions{HashBytes: 8, BlockSize: 4})
		got := mustDecode(t, data)

		assert.Nil(t, got.Metadata.AllTargetsFileHashes)
	})
}

// ─── Test: property test over random small graphs ─────────────────────────────

func TestRandomGraphRoundTrip(t *testing.T) {
	// 200 random graphs of up to 500 nodes.
	const numGraphs = 200
	const maxNodes = 500

	rng := rand.New(rand.NewSource(42))

	for trial := 0; trial < numGraphs; trial++ {
		g := randomGraph(rng, rng.Intn(maxNodes)+1)
		opts := tgb.EncodeOptions{HashBytes: 8, BlockSize: 16}
		data := mustEncode(t, g, opts)
		got := mustDecode(t, data)

		want := canonicalise(tgb.TruncateHashes(g, 8))
		got2 := canonicalise(got)

		if !reflect.DeepEqual(want, got2) {
			t.Errorf("trial %d (n=%d): round-trip mismatch", trial, len(g.Targets))
			if len(want.Targets) != len(got2.Targets) {
				t.Logf("  target count: want %d got %d", len(want.Targets), len(got2.Targets))
				continue
			}
			for i := range want.Targets {
				if !reflect.DeepEqual(want.Targets[i], got2.Targets[i]) {
					t.Logf("  target %d: want %+v", i, want.Targets[i])
					t.Logf("  target %d:  got %+v", i, got2.Targets[i])
				}
			}
		}
	}
}

func randomGraph(rng *rand.Rand, n int) *tgb.Graph {
	// Generate n unique labels.
	pkgs := []string{
		"//src/foo/bar", "//src/foo/baz", "//src/foo/qux",
		"//src/alpha/beta", "//src/alpha/gamma",
		"//third_party/go/lib",
	}
	names := []string{"lib", "bin", "test", "a.go", "b.go", "c.go", "main.go", "util"}
	ruleTypes := map[int32]string{0: "source file", 1: "go_library", 2: "go_binary", 3: "go_test"}
	tagMapping := map[int32]string{0: "manual", 1: "no-remote", 2: "local"}
	anMapping := map[int32]string{0: "visibility", 1: "deprecated"}
	avMapping := map[int32]string{0: "//visibility:public", 1: "true", 2: "false"}

	idMap := make(map[int32]string, n)
	usedLabels := make(map[string]bool, n)
	targets := make([]entity.OptimizedTarget, n)

	for i := 0; i < n; i++ {
		var label string
		for {
			pkg := pkgs[rng.Intn(len(pkgs))]
			name := fmt.Sprintf("%s_%d", names[rng.Intn(len(names))], rng.Intn(10000))
			label = pkg + ":" + name
			if !usedLabels[label] {
				break
			}
		}
		usedLabels[label] = true
		id := int32(i + 1)
		idMap[id] = label

		// Random hash.
		raw := make([]byte, 20)
		rng.Read(raw)
		hash := hex.EncodeToString(raw)

		// Random rule type.
		rt := int32(rng.Intn(4))

		// Random deps (only to earlier nodes to avoid self-loops easily).
		var deps []int32
		if i > 0 {
			ndeps := rng.Intn(min(i+1, 8))
			depSet := make(map[int32]bool)
			for j := 0; j < ndeps; j++ {
				d := int32(rng.Intn(i) + 1) // ID 1..i
				depSet[d] = true
			}
			for d := range depSet {
				deps = append(deps, d)
			}
		}

		// Random tags.
		var tags []int32
		if rng.Intn(3) == 0 {
			tags = []int32{int32(rng.Intn(3))}
		}

		// Random attrs.
		attrs := map[int32]int32{}
		if rng.Intn(4) == 0 {
			attrs[int32(rng.Intn(2))] = int32(rng.Intn(3))
		}

		targets[i] = entity.OptimizedTarget{
			ID:                 id,
			Hash:               hash,
			DirectDependencies: deps,
			RuleType:           rt,
			Tags:               tags,
			Root:               rng.Intn(5) == 0,
			External:           rng.Intn(10) == 0,
			Attributes:         attrs,
		}
	}

	return &tgb.Graph{
		Targets: targets,
		Metadata: entity.Metadata{
			TargetIDMapping:             idMap,
			RuleTypeMapping:             ruleTypes,
			TagMapping:                  tagMapping,
			AttributeNameMapping:        anMapping,
			AttributeStringValueMapping: avMapping,
		},
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─── Test: Reader accessors agree with Decode ─────────────────────────────────

func TestReaderVsDecode(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	g := randomGraph(rng, 300)
	opts := tgb.EncodeOptions{HashBytes: 8, BlockSize: 32}
	data := mustEncode(t, g, opts)

	decoded := mustDecode(t, data)
	r, err := tgb.NewReader(data)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	n := r.NodeCount()
	if n != len(decoded.Targets) {
		t.Fatalf("NodeCount: reader=%d decode=%d", n, len(decoded.Targets))
	}

	// For each node, compare Reader accessors against Decode output.
	for i := 0; i < n; i++ {
		dt := decoded.Targets[i]
		label := decoded.Metadata.TargetIDMapping[dt.ID]

		// Label
		rLabel := r.Label(i)
		if rLabel != label {
			t.Errorf("node %d Label: reader=%q decode=%q", i, rLabel, label)
		}

		// Hash (first 8 bytes)
		hb := r.HashBytes()
		hashes := r.Hashes()
		rawHash := hashes[i*hb : (i+1)*hb]
		wantHash := dt.Hash // already truncated by Decode
		gotHash := hex.EncodeToString(rawHash)
		if gotHash != wantHash {
			t.Errorf("node %d Hash: reader=%q decode=%q", i, gotHash, wantHash)
		}

		// Deps
		rDeps := r.Deps(i, nil)
		dDeps := dt.DirectDependencies
		if !int32SliceEq(rDeps, dDeps) {
			t.Errorf("node %d Deps: reader=%v decode=%v", i, rDeps, dDeps)
		}

		// Root / External
		if r.Root(i) != dt.Root {
			t.Errorf("node %d Root: reader=%v decode=%v", i, r.Root(i), dt.Root)
		}
		if r.External(i) != dt.External {
			t.Errorf("node %d External: reader=%v decode=%v", i, r.External(i), dt.External)
		}
	}
}

func int32SliceEq(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	ac := make([]int32, len(a))
	bc := make([]int32, len(b))
	copy(ac, a)
	copy(bc, b)
	sort.Slice(ac, func(i, j int) bool { return ac[i] < ac[j] })
	sort.Slice(bc, func(i, j int) bool { return bc[i] < bc[j] })
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

// ─── Test: lazy decompression ─────────────────────────────────────────────────

// TestHashesOnlyDoesNotDecompress verifies that constructing a Reader and
// reading only Hashes() does not decompress any zstd-coded column.
// We instrument via r.DecompressionCount.
func TestHashesOnlyDoesNotDecompress(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	g := randomGraph(rng, 50)
	opts := tgb.EncodeOptions{HashBytes: 8, BlockSize: 16}
	data := mustEncode(t, g, opts)

	r, err := tgb.NewReader(data)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	// Read only Hashes() — this is a raw column; no decompression needed.
	_ = r.Hashes()
	_ = r.NodeCount()
	_ = r.HashBytes()
	_ = r.BlockDigests() // also raw

	if r.DecompressionCount != 0 {
		t.Errorf("expected 0 decompressions after Hashes()+NodeCount()+BlockDigests(), got %d",
			r.DecompressionCount)
	}
}

// ─── Test: ColumnStats helper ────────────────────────────────────────────────

func TestColumnStats(t *testing.T) {
	g := buildTinyGraph()
	opts := tgb.EncodeOptions{HashBytes: 8, BlockSize: 4}
	data := mustEncode(t, g, opts)

	stats, err := tgb.ColumnStats(data)
	if err != nil {
		t.Fatalf("ColumnStats: %v", err)
	}
	if len(stats) == 0 {
		t.Fatal("expected at least one column stat")
	}
	t.Log("Column stats:")
	for _, s := range stats {
		t.Logf("  id=%-2d %-20s raw=%-8d comp=%d", s.ID, s.Name, s.RawSize, s.CompressedSize)
	}
}
