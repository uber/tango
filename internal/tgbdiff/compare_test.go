package tgbdiff_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/tgb"
	"github.com/uber/tango/internal/tgbdiff"
)

// ─── graph builder helpers ────────────────────────────────────────────────────

// syntheticGraph builds a small deterministic tgb.Graph with the given
// node count and edge density (0.0 = no edges, 1.0 = dense).
// rng controls randomness. Labels are sorted so TGB encode produces
// a canonical ordering.
func syntheticGraph(rng *rand.Rand, n int, density float64) *tgb.Graph {
	if n == 0 {
		return &tgb.Graph{
			Metadata: entity.Metadata{
				TargetIDMapping:             map[int32]string{},
				RuleTypeMapping:             map[int32]string{1: "go_library", 2: "source file"},
				TagMapping:                  map[int32]string{},
				AttributeNameMapping:        map[int32]string{1: "visibility", 2: "testonly"},
				AttributeStringValueMapping: map[int32]string{1: "//visibility:public", 2: "True"},
			},
		}
	}

	// Generate unique labels sorted so TGB sort order == slice order.
	labels := make([]string, n)
	for i := 0; i < n; i++ {
		pkg := fmt.Sprintf("//src/pkg%04d", i/5)
		name := fmt.Sprintf("target%04d", i)
		labels[i] = pkg + ":" + name
	}
	sort.Strings(labels)

	// Rule type mapping.
	rtMap := map[int32]string{1: "go_library", 2: "source file"}
	tagMap := map[int32]string{1: "manual", 2: "no-remote"}
	anMap := map[int32]string{1: "visibility", 2: "testonly"}
	avMap := map[int32]string{1: "//visibility:public", 2: "True"}

	targets := make([]entity.OptimizedTarget, n)
	idMap := make(map[int32]string, n)

	for i := 0; i < n; i++ {
		id := int32(i + 1)
		idMap[id] = labels[i]

		// Rule type: every 5th node is a source file.
		rt := int32(1)
		if i%5 == 0 {
			rt = 2
		}

		// Hash: deterministic from label.
		h := labelHash(labels[i])

		// Deps: randomly pick backward edges (to keep a DAG).
		var deps []int32
		if i > 0 && density > 0 {
			maxDeps := i
			if maxDeps > 10 {
				maxDeps = 10
			}
			for j := 0; j < maxDeps; j++ {
				if rng.Float64() < density {
					depIdx := rng.Intn(i)
					depID := int32(depIdx + 1)
					deps = append(deps, depID)
				}
			}
			// Deduplicate.
			deps = dedupInt32(deps)
			sort.Slice(deps, func(a, b int) bool { return deps[a] < deps[b] })
		}

		// Attrs: give every other node a visibility attr.
		var attrs map[int32]int32
		if i%2 == 0 {
			attrs = map[int32]int32{1: 1} // visibility=public
		} else {
			attrs = map[int32]int32{}
		}

		// Tags: some nodes get tags.
		var tags []int32
		if i%7 == 0 {
			tags = []int32{1} // "manual"
		}

		targets[i] = entity.OptimizedTarget{
			ID:                 id,
			Hash:               h,
			DirectDependencies: deps,
			RuleType:           rt,
			Tags:               tags,
			Root:               rt == 1 && i%20 == 0,
			External:           false,
			Attributes:         attrs,
		}
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

// labelHash produces a deterministic 40-char hex hash from a label string.
func labelHash(label string) string {
	// Simple but deterministic: mix bytes.
	var h [20]byte
	for i, b := range []byte(label) {
		h[i%20] ^= b + byte(i*7)
	}
	// Ensure non-zero.
	h[0] |= 1
	return fmt.Sprintf("%x", h)
}

func dedupInt32(s []int32) []int32 {
	if len(s) == 0 {
		return s
	}
	seen := make(map[int32]struct{}, len(s))
	out := s[:0]
	for _, v := range s {
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// ─── encode helpers ───────────────────────────────────────────────────────────

// encodeGraph encodes g and returns a tgb.Reader over the blob.
// blockSize controls how many nodes per digest block.
func encodeGraph(t *testing.T, g *tgb.Graph, blockSize int) *tgb.Reader {
	t.Helper()
	if blockSize == 0 {
		blockSize = 64 // small for tests
	}
	var buf bytes.Buffer
	opts := tgb.EncodeOptions{HashBytes: 8, BlockSize: blockSize}
	if err := tgb.Encode(&buf, g, opts); err != nil {
		t.Fatalf("tgb.Encode: %v", err)
	}
	r, err := tgb.NewReader(buf.Bytes())
	if err != nil {
		t.Fatalf("tgb.NewReader: %v", err)
	}
	return r
}

// compareOpts returns a standard Options for tests.
func compareOpts() tgbdiff.Options {
	return tgbdiff.Options{MaxDistance: -1}
}

// ─── differential test helper ─────────────────────────────────────────────────

// diffEqual encodes both graphs, runs tgbdiff and the targetdiff oracle, and
// asserts equality.
// NOTE: these tests encode at 8-byte hashes. The oracle is fed
// TruncateHashes(g, 8) so both sides see the same truncated hashes; without
// this, targetdiff would compare full 40-char hex hashes while tgbdiff
// compares the 8-byte truncated ones, causing false differences.
func diffEqual(t *testing.T, name string, bGraph, aGraph *tgb.Graph, blockSize int) {
	t.Helper()

	// Truncate hashes for the oracle to match what these blobs store (8 bytes
	// = 16 hex chars).
	bTrunc := tgb.TruncateHashes(bGraph, 8)
	aTrunc := tgb.TruncateHashes(aGraph, 8)

	// Encode.
	bReader := encodeGraph(t, bGraph, blockSize)
	aReader := encodeGraph(t, aGraph, blockSize)

	ctx := context.Background()
	opts := compareOpts()

	// Run tgbdiff.
	fastResult, err := tgbdiff.Compare(ctx, bReader, aReader, opts)
	if err != nil {
		t.Fatalf("%s: tgbdiff.Compare: %v", name, err)
	}

	// Run targetdiff (correctness oracle).
	refResult := oracleCompare(t, bTrunc, aTrunc, nil, -1)

	if !reflect.DeepEqual(fastResult, refResult) {
		t.Errorf("%s: tgbdiff result differs from targetdiff oracle", name)
		t.Logf("  tgbdiff:    %d changed", len(fastResult.Changed))
		t.Logf("  targetdiff: %d changed", len(refResult.Changed))
		// Show first few differences.
		fastByLabel := make(map[string]tgbdiff.ChangedTarget, len(fastResult.Changed))
		for _, c := range fastResult.Changed {
			fastByLabel[c.Label] = c
		}
		refByLabel := make(map[string]tgbdiff.ChangedTarget, len(refResult.Changed))
		for _, c := range refResult.Changed {
			refByLabel[c.Label] = c
		}
		shown := 0
		for label, rc := range refByLabel {
			fc, ok := fastByLabel[label]
			if !ok {
				t.Logf("  MISSING in fastdiff: %s (ref: %+v)", label, rc)
				shown++
			} else if fc != rc {
				t.Logf("  MISMATCH %s: fast=%+v ref=%+v", label, fc, rc)
				shown++
			}
			if shown >= 10 {
				break
			}
		}
		for label, fc := range fastByLabel {
			if _, ok := refByLabel[label]; !ok {
				t.Logf("  EXTRA in fastdiff: %s (fast: %+v)", label, fc)
				shown++
				if shown >= 15 {
					break
				}
			}
		}
	}
}

// ─── test cases ───────────────────────────────────────────────────────────────

// TestIdentical verifies that two identical graphs produce an empty diff.
func TestIdentical(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for _, n := range []int{0, 1, 10, 100, 500} {
		g := syntheticGraph(rng, n, 0.3)
		diffEqual(t, fmt.Sprintf("identical/n=%d", n), g, g, 64)
	}
}

// TestSingleLeafChange verifies a single source-file change.
func TestSingleLeafChange(t *testing.T) {
	rng := rand.New(rand.NewSource(100))
	for _, n := range []int{10, 100, 500, 2000} {
		before := syntheticGraph(rng, n, 0.2)
		after := deepCopyGraph(before)
		// Change hash of first source-file node.
		for i := range after.Targets {
			rt := after.Metadata.RuleTypeMapping[after.Targets[i].RuleType]
			if rt == "source file" {
				after.Targets[i].Hash = perturbHash(after.Targets[i].Hash)
				break
			}
		}
		recomputeHashes(after)
		diffEqual(t, fmt.Sprintf("single_leaf/n=%d", n), before, after, 64)
	}
}

// TestNoChange verifies that an unchanged pair produces empty diff.
func TestNoChange(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	g := syntheticGraph(rng, 200, 0.3)
	diffEqual(t, "no_change", g, g, 64)
}

// TestAddTargets verifies that added targets appear as New.
func TestAddTargets(t *testing.T) {
	rng := rand.New(rand.NewSource(200))
	before := syntheticGraph(rng, 50, 0.2)
	after := syntheticGraph(rng, 70, 0.2) // bigger graph with new nodes
	diffEqual(t, "add_targets", before, after, 64)
}

// TestDeleteTargets verifies that deleted targets appear as Deleted.
func TestDeleteTargets(t *testing.T) {
	rng := rand.New(rand.NewSource(300))
	before := syntheticGraph(rng, 70, 0.2)
	after := syntheticGraph(rng, 50, 0.2) // smaller
	diffEqual(t, "delete_targets", before, after, 64)
}

// TestAddAndDeleteAndChange exercises all three change types at once.
func TestAddAndDeleteAndChange(t *testing.T) {
	rng := rand.New(rand.NewSource(400))
	before := syntheticGraph(rng, 100, 0.3)
	after := deepCopyGraph(before)
	// Delete some targets (remove last 10).
	after.Targets = after.Targets[:90]
	for _, t2 := range after.Targets[90:] {
		delete(after.Metadata.TargetIDMapping, t2.ID)
	}
	// Change some targets.
	for i := 0; i < 5; i++ {
		after.Targets[i].Hash = perturbHash(after.Targets[i].Hash)
	}
	recomputeHashes(after)
	diffEqual(t, "mixed_changes", before, after, 64)
}

// TestWholePackageChange changes all nodes in one package.
func TestWholePackageChange(t *testing.T) {
	rng := rand.New(rand.NewSource(500))
	before := syntheticGraph(rng, 200, 0.2)
	after := deepCopyGraph(before)
	// Find a package and mutate all its source files.
	pkg := "//src/pkg0000"
	for i := range after.Targets {
		label := after.Metadata.TargetIDMapping[after.Targets[i].ID]
		if len(label) > len(pkg) && label[:len(pkg)] == pkg {
			rt := after.Metadata.RuleTypeMapping[after.Targets[i].RuleType]
			if rt == "source file" {
				after.Targets[i].Hash = perturbHash(after.Targets[i].Hash)
			}
		}
	}
	recomputeHashes(after)
	diffEqual(t, "whole_package", before, after, 64)
}

// TestSmallGraph verifies correctness on a single-block graph.
func TestSmallGraph(t *testing.T) {
	rng := rand.New(rand.NewSource(600))
	for _, n := range []int{1, 2, 3, 5, 8, 15, 30} {
		before := syntheticGraph(rng, n, 0.3)
		after := deepCopyGraph(before)
		if n > 0 {
			after.Targets[0].Hash = perturbHash(after.Targets[0].Hash)
			recomputeHashes(after)
		}
		diffEqual(t, fmt.Sprintf("small/n=%d", n), before, after, 4)
	}
}

// TestBlockBoundaryInsertStart inserts a node at the very start of a block.
func TestBlockBoundaryInsertStart(t *testing.T) {
	rng := rand.New(rand.NewSource(700))
	before := syntheticGraph(rng, 100, 0.1)
	// after adds a node that sorts before all existing labels.
	after := deepCopyGraph(before)
	maxID := int32(0)
	for _, t2 := range after.Targets {
		if t2.ID > maxID {
			maxID = t2.ID
		}
	}
	newID := maxID + 1
	newLabel := "//src/aaa:aaa" // sorts before //src/pkg...
	after.Targets = append(after.Targets, entity.OptimizedTarget{
		ID:         newID,
		Hash:       labelHash(newLabel),
		RuleType:   1,
		Attributes: map[int32]int32{},
	})
	after.Metadata.TargetIDMapping[newID] = newLabel
	sort.Slice(after.Targets, func(i, j int) bool {
		la := after.Metadata.TargetIDMapping[after.Targets[i].ID]
		lb := after.Metadata.TargetIDMapping[after.Targets[j].ID]
		return la < lb
	})
	recomputeHashes(after)
	diffEqual(t, "insert_start", before, after, 16)
}

// TestBlockBoundaryInsertEnd inserts a node at the very end.
func TestBlockBoundaryInsertEnd(t *testing.T) {
	rng := rand.New(rand.NewSource(800))
	before := syntheticGraph(rng, 100, 0.1)
	after := deepCopyGraph(before)
	maxID := int32(0)
	for _, t2 := range after.Targets {
		if t2.ID > maxID {
			maxID = t2.ID
		}
	}
	newID := maxID + 1
	newLabel := "//src/zzz:zzz" // sorts after all existing
	after.Targets = append(after.Targets, entity.OptimizedTarget{
		ID:         newID,
		Hash:       labelHash(newLabel),
		RuleType:   1,
		Attributes: map[int32]int32{},
	})
	after.Metadata.TargetIDMapping[newID] = newLabel
	recomputeHashes(after)
	diffEqual(t, "insert_end", before, after, 16)
}

// TestBlockBoundaryInsertMiddle inserts a node in the middle of a block.
func TestBlockBoundaryInsertMiddle(t *testing.T) {
	rng := rand.New(rand.NewSource(900))
	before := syntheticGraph(rng, 100, 0.1)
	after := deepCopyGraph(before)
	maxID := int32(0)
	for _, t2 := range after.Targets {
		if t2.ID > maxID {
			maxID = t2.ID
		}
	}
	newID := maxID + 1
	newLabel := "//src/pkg0002:inserted_middle"
	after.Targets = append(after.Targets, entity.OptimizedTarget{
		ID:         newID,
		Hash:       labelHash(newLabel),
		RuleType:   1,
		Attributes: map[int32]int32{},
	})
	after.Metadata.TargetIDMapping[newID] = newLabel
	sort.Slice(after.Targets, func(i, j int) bool {
		la := after.Metadata.TargetIDMapping[after.Targets[i].ID]
		lb := after.Metadata.TargetIDMapping[after.Targets[j].ID]
		return la < lb
	})
	recomputeHashes(after)
	diffEqual(t, "insert_middle", before, after, 16)
}

// TestDeleteFirst deletes the very first node.
func TestDeleteFirst(t *testing.T) {
	rng := rand.New(rand.NewSource(1000))
	before := syntheticGraph(rng, 50, 0.1)
	after := deepCopyGraph(before)
	deleted := after.Targets[0].ID
	delete(after.Metadata.TargetIDMapping, deleted)
	// Remove from dep lists.
	newTargets := after.Targets[1:]
	for i := range newTargets {
		var deps []int32
		for _, d := range newTargets[i].DirectDependencies {
			if d != deleted {
				deps = append(deps, d)
			}
		}
		newTargets[i].DirectDependencies = deps
	}
	after.Targets = newTargets
	recomputeHashes(after)
	diffEqual(t, "delete_first", before, after, 16)
}

// TestDeleteLast deletes the last node.
func TestDeleteLast(t *testing.T) {
	rng := rand.New(rand.NewSource(1100))
	before := syntheticGraph(rng, 50, 0.1)
	after := deepCopyGraph(before)
	last := after.Targets[len(after.Targets)-1].ID
	delete(after.Metadata.TargetIDMapping, last)
	after.Targets = after.Targets[:len(after.Targets)-1]
	for i := range after.Targets {
		var deps []int32
		for _, d := range after.Targets[i].DirectDependencies {
			if d != last {
				deps = append(deps, d)
			}
		}
		after.Targets[i].DirectDependencies = deps
	}
	recomputeHashes(after)
	diffEqual(t, "delete_last", before, after, 16)
}

// ─── counter behaviour tests ──────────────────────────────────────────────────

// TestCountersIdentical asserts that comparing an identical pair skips
// essentially all blocks and decodes ~0 node labels. This is the central
// performance property of the design.
func TestCountersIdentical(t *testing.T) {
	rng := rand.New(rand.NewSource(9999))
	g := syntheticGraph(rng, 500, 0.2)

	bReader := encodeGraph(t, g, 64)
	aReader := encodeGraph(t, g, 64)

	ctx := context.Background()
	_, _, cnt, err := tgbdiff.CompareInstrumented(ctx, bReader, aReader, compareOpts())
	if err != nil {
		t.Fatalf("CompareInstrumented: %v", err)
	}

	t.Logf("identical pair counters: %+v", cnt)

	// All blocks should be skipped (zero decoded).
	if cnt.BlocksDecoded != 0 {
		t.Errorf("identical pair: BlocksDecoded = %d, want 0", cnt.BlocksDecoded)
	}
	if cnt.BlocksSkipped == 0 {
		t.Errorf("identical pair: BlocksSkipped = 0, want > 0 (some blocks must exist)")
	}
	// No labels decoded in phase 0/1 (all hash-equal, no bisect needed).
	// NodesLabelDecoded may be non-zero only if BFS traverses labels;
	// for identical graphs there are no changed nodes so BFS does nothing.
	if cnt.ChangedNodes != 0 {
		t.Errorf("identical pair: ChangedNodes = %d, want 0", cnt.ChangedNodes)
	}
	// Phase 0 produces no deletes/inserts → NodesLabelDecoded == 0.
	// Phase 1 detects no changes → no bisect labels decoded.
	// Phase 2 has nothing to classify.
	// Phase 3 BFS has no seeds.
	if cnt.NodesLabelDecoded != 0 {
		t.Errorf("identical pair: NodesLabelDecoded = %d, want 0", cnt.NodesLabelDecoded)
	}
}

// TestCountersHeavilyChanged asserts that a heavily-changed pair decodes more
// nodes and has more seeds. We don't fix exact numbers but check ordering.
func TestCountersHeavilyChanged(t *testing.T) {
	rng := rand.New(rand.NewSource(1234))
	before := syntheticGraph(rng, 200, 0.3)
	after := deepCopyGraph(before)
	// Mutate every node's hash.
	for i := range after.Targets {
		after.Targets[i].Hash = perturbHash(after.Targets[i].Hash)
	}
	recomputeHashes(after)

	bReader := encodeGraph(t, before, 32)
	aReader := encodeGraph(t, after, 32)

	ctx := context.Background()
	_, _, cnt, err := tgbdiff.CompareInstrumented(ctx, bReader, aReader, compareOpts())
	if err != nil {
		t.Fatalf("CompareInstrumented: %v", err)
	}

	t.Logf("heavily changed pair counters: %+v", cnt)

	if cnt.ChangedNodes == 0 {
		t.Errorf("heavily changed pair: ChangedNodes = 0, want > 0")
	}
	if cnt.Seeds == 0 {
		t.Errorf("heavily changed pair: Seeds = 0, want > 0")
	}
}

// ─── randomised differential test ────────────────────────────────────────────

// TestDifferentialRandom is the centrepiece differential test. It generates
// many random graph pairs across a variety of shapes and asserts that
// fastdiff and refdiff always produce identical results.
func TestDifferentialRandom(t *testing.T) {
	type testCase struct {
		name      string
		seed      int64
		beforeN   int
		afterN    int
		density   float64
		blockSize int
		mutate    func(before, after *tgb.Graph, rng *rand.Rand)
	}

	// Mutation helpers for the table.
	noMutation := func(_, _ *tgb.Graph, _ *rand.Rand) {}

	changeOneLeaf := func(before, after *tgb.Graph, rng *rand.Rand) {
		_ = before
		// Change one source-file node.
		for i := range after.Targets {
			rt := after.Metadata.RuleTypeMapping[after.Targets[i].RuleType]
			if rt == "source file" {
				after.Targets[i].Hash = perturbHash(after.Targets[i].Hash)
				break
			}
		}
		recomputeHashes(after)
	}

	changeMany := func(before, after *tgb.Graph, rng *rand.Rand) {
		_ = before
		// Change roughly 10% of all nodes.
		for i := range after.Targets {
			if rng.Float64() < 0.1 {
				after.Targets[i].Hash = perturbHash(after.Targets[i].Hash)
			}
		}
		recomputeHashes(after)
	}

	changeAll := func(before, after *tgb.Graph, rng *rand.Rand) {
		_ = before
		for i := range after.Targets {
			after.Targets[i].Hash = perturbHash(after.Targets[i].Hash)
		}
		recomputeHashes(after)
	}

	cases := []testCase{
		// Identical graphs — no change.
		{"identical/tiny", 1, 10, 10, 0.1, 4, noMutation},
		{"identical/small", 2, 100, 100, 0.2, 16, noMutation},
		{"identical/medium", 3, 1000, 1000, 0.1, 64, noMutation},
		{"identical/large", 4, 5000, 5000, 0.05, 128, noMutation},

		// Single leaf change.
		{"leaf/tiny", 10, 10, 10, 0.1, 4, changeOneLeaf},
		{"leaf/small", 11, 100, 100, 0.2, 16, changeOneLeaf},
		{"leaf/medium", 12, 1000, 1000, 0.1, 64, changeOneLeaf},
		{"leaf/large", 13, 5000, 5000, 0.05, 128, changeOneLeaf},

		// Many changes (~10%).
		{"many/tiny", 20, 10, 10, 0.3, 4, changeMany},
		{"many/small", 21, 100, 100, 0.3, 16, changeMany},
		{"many/medium", 22, 1000, 1000, 0.2, 64, changeMany},
		{"many/large", 23, 5000, 5000, 0.1, 128, changeMany},

		// All changed — wide blast radius.
		{"all/tiny", 30, 10, 10, 0.2, 4, changeAll},
		{"all/small", 31, 100, 100, 0.2, 16, changeAll},
		{"all/medium", 32, 500, 500, 0.2, 32, changeAll},

		// Graph size changes (additions + deletions).
		{"add/small", 40, 50, 80, 0.1, 8, noMutation},
		{"add/medium", 41, 200, 300, 0.1, 32, noMutation},
		{"add/large", 42, 1000, 1500, 0.05, 64, noMutation},
		{"del/small", 50, 80, 50, 0.1, 8, noMutation},
		{"del/medium", 51, 300, 200, 0.1, 32, noMutation},
		{"del/large", 52, 1500, 1000, 0.05, 64, noMutation},

		// Additions + deletions + changes combined.
		{"mixed/small", 60, 80, 90, 0.2, 8, changeMany},
		{"mixed/medium", 61, 300, 350, 0.2, 32, changeMany},
		{"mixed/large", 62, 1000, 1200, 0.1, 64, changeMany},

		// Block-boundary stress: very small block size.
		{"block_stress/size1", 70, 50, 50, 0.1, 1, changeOneLeaf},
		{"block_stress/size2", 71, 100, 100, 0.1, 2, changeMany},
		{"block_stress/size4", 72, 200, 200, 0.1, 4, changeMany},
		{"block_stress/size8", 73, 500, 500, 0.1, 8, changeMany},

		// One block (entire graph fits in one block).
		{"one_block/8nodes", 80, 8, 8, 0.3, 64, changeMany},
		{"one_block/3nodes", 81, 3, 3, 0.5, 64, changeAll},

		// Zero-node graphs.
		{"empty/both", 90, 0, 0, 0.0, 16, noMutation},
		{"empty/before", 91, 0, 20, 0.0, 16, noMutation},
		{"empty/after", 92, 20, 0, 0.0, 16, noMutation},

		// High-density edges.
		{"dense/small", 100, 50, 50, 0.8, 16, changeMany},
		{"dense/medium", 101, 200, 200, 0.7, 32, changeMany},

		// Large graphs.
		{"large/5k_leaf", 110, 5000, 5000, 0.05, 64, changeOneLeaf},
		{"large/5k_many", 111, 5000, 5000, 0.05, 64, changeMany},
		{"large/10k_leaf", 112, 10000, 10000, 0.03, 128, changeOneLeaf},
		{"large/20k_leaf", 113, 20000, 20000, 0.02, 256, changeOneLeaf},
		{"large/20k_many", 114, 20000, 20000, 0.02, 256, changeMany},
	}

	total := 0
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rng := rand.New(rand.NewSource(tc.seed))
			before := syntheticGraph(rng, tc.beforeN, tc.density)
			// For size-change tests, use a different seed for after.
			afterRng := rand.New(rand.NewSource(tc.seed + 1000))
			after := syntheticGraph(afterRng, tc.afterN, tc.density)

			// Apply mutation.
			tc.mutate(before, after, rng)

			diffEqual(t, tc.name, before, after, tc.blockSize)
			total++
		})
	}
	t.Logf("ran %d random differential test pairs", total)
}

// ─── additional random sweep ──────────────────────────────────────────────────

// TestDifferentialRandomSweep runs a large number of tiny random pairs to catch
// edge cases not covered by the table above.
func TestDifferentialRandomSweep(t *testing.T) {
	const numPairs = 200
	rng := rand.New(rand.NewSource(0xDEADBEEF))

	passed := 0
	for i := 0; i < numPairs; i++ {
		seed := rng.Int63()
		pairRng := rand.New(rand.NewSource(seed))

		beforeN := pairRng.Intn(200) + 1
		afterN := pairRng.Intn(200) + 1
		density := pairRng.Float64() * 0.5
		blockSize := 1 << uint(pairRng.Intn(6)) // 1, 2, 4, 8, 16, 32

		before := syntheticGraph(pairRng, beforeN, density)
		after := syntheticGraph(rand.New(rand.NewSource(seed+1)), afterN, density)

		// Random mutation: 0=none, 1=change one, 2=change many, 3=change all.
		switch pairRng.Intn(4) {
		case 1:
			if len(after.Targets) > 0 {
				after.Targets[0].Hash = perturbHash(after.Targets[0].Hash)
				recomputeHashes(after)
			}
		case 2:
			for j := range after.Targets {
				if pairRng.Float64() < 0.15 {
					after.Targets[j].Hash = perturbHash(after.Targets[j].Hash)
				}
			}
			recomputeHashes(after)
		case 3:
			for j := range after.Targets {
				after.Targets[j].Hash = perturbHash(after.Targets[j].Hash)
			}
			recomputeHashes(after)
		}

		name := fmt.Sprintf("sweep/%d(b=%d,a=%d,bs=%d)", i, beforeN, afterN, blockSize)
		diffEqual(t, name, before, after, blockSize)
		passed++
	}
	t.Logf("sweep: %d pairs passed", passed)
}

// ─── graph mutation helpers ───────────────────────────────────────────────────

// deepCopyGraph returns a deep copy of g suitable for mutation.
func deepCopyGraph(g *tgb.Graph) *tgb.Graph {
	ng := &tgb.Graph{
		Targets: make([]entity.OptimizedTarget, len(g.Targets)),
		Metadata: entity.Metadata{
			TargetIDMapping:             make(map[int32]string, len(g.Metadata.TargetIDMapping)),
			RuleTypeMapping:             make(map[int32]string, len(g.Metadata.RuleTypeMapping)),
			TagMapping:                  make(map[int32]string, len(g.Metadata.TagMapping)),
			AttributeNameMapping:        make(map[int32]string, len(g.Metadata.AttributeNameMapping)),
			AttributeStringValueMapping: make(map[int32]string, len(g.Metadata.AttributeStringValueMapping)),
		},
	}
	for i, t := range g.Targets {
		nt := entity.OptimizedTarget{
			ID:       t.ID,
			Hash:     t.Hash,
			RuleType: t.RuleType,
			Root:     t.Root,
			External: t.External,
		}
		if len(t.DirectDependencies) > 0 {
			nt.DirectDependencies = make([]int32, len(t.DirectDependencies))
			copy(nt.DirectDependencies, t.DirectDependencies)
		}
		if len(t.Tags) > 0 {
			nt.Tags = make([]int32, len(t.Tags))
			copy(nt.Tags, t.Tags)
		}
		if len(t.Attributes) > 0 {
			nt.Attributes = make(map[int32]int32, len(t.Attributes))
			for k, v := range t.Attributes {
				nt.Attributes[k] = v
			}
		}
		ng.Targets[i] = nt
	}
	for k, v := range g.Metadata.TargetIDMapping {
		ng.Metadata.TargetIDMapping[k] = v
	}
	for k, v := range g.Metadata.RuleTypeMapping {
		ng.Metadata.RuleTypeMapping[k] = v
	}
	for k, v := range g.Metadata.TagMapping {
		ng.Metadata.TagMapping[k] = v
	}
	for k, v := range g.Metadata.AttributeNameMapping {
		ng.Metadata.AttributeNameMapping[k] = v
	}
	for k, v := range g.Metadata.AttributeStringValueMapping {
		ng.Metadata.AttributeStringValueMapping[k] = v
	}
	return ng
}

// perturbHash flips a single nibble in a hex hash string deterministically.
func perturbHash(old string) string {
	if len(old) < 2 {
		return "ff" + old
	}
	b := []byte(old)
	// XOR first byte to ensure it changes.
	orig := b[0]
	choices := []byte("0123456789abcdef")
	for _, c := range choices {
		if c != orig {
			b[0] = c
			break
		}
	}
	return string(b)
}

// recomputeHashes re-runs full Merkle hash propagation over g in place, so a
// mutation to one target's inputs ripples to its transitive dependents the
// way tango's producer would propagate it. The exact hash recipe doesn't
// matter — both the oracle and tgbdiff see the same strings — only that it is
// deterministic and dependency-sensitive.
func recomputeHashes(g *tgb.Graph) {
	n := len(g.Targets)
	idToIdx := make(map[int32]int, n)
	for i, t := range g.Targets {
		idToIdx[t.ID] = i
	}

	inDeg := make([]int, n)
	revAdj := make([][]int, n)
	for i, t := range g.Targets {
		for _, dep := range t.DirectDependencies {
			if depIdx, ok := idToIdx[dep]; ok {
				inDeg[depIdx]++
				revAdj[depIdx] = append(revAdj[depIdx], i)
			}
		}
	}

	queue := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if inDeg[i] == 0 {
			queue = append(queue, i)
		}
	}

	hashes := make([][20]byte, n)
	processed := make([]bool, n)
	hashOne := func(idx int, withDeps bool) {
		t := &g.Targets[idx]
		h := sha1.New()
		bodyBuf := make([]byte, 8)
		binary.LittleEndian.PutUint32(bodyBuf[:4], uint32(t.ID))
		binary.LittleEndian.PutUint32(bodyBuf[4:], uint32(t.RuleType))
		h.Write(bodyBuf)
		if withDeps {
			sortedDeps := make([]int32, len(t.DirectDependencies))
			copy(sortedDeps, t.DirectDependencies)
			sort.Slice(sortedDeps, func(a, b int) bool { return sortedDeps[a] < sortedDeps[b] })
			for _, depID := range sortedDeps {
				if depIdx, ok := idToIdx[depID]; ok {
					h.Write(hashes[depIdx][:])
				}
			}
		}
		copy(hashes[idx][:], h.Sum(nil))
		t.Hash = fmt.Sprintf("%x", hashes[idx])
	}

	for len(queue) > 0 {
		idx := queue[0]
		queue = queue[1:]
		processed[idx] = true
		hashOne(idx, true)
		for _, depIdx := range revAdj[idx] {
			inDeg[depIdx]--
			if inDeg[depIdx] == 0 {
				queue = append(queue, depIdx)
			}
		}
	}

	// Nodes on cycles never reach in-degree zero — hash them without dep
	// hashes, mirroring the sentinel-ish "break the cycle somewhere" shape.
	for i := range g.Targets {
		if !processed[i] {
			hashOne(i, false)
		}
	}
}
