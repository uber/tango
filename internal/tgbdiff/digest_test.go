package tgbdiff_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/tgb"
	"github.com/uber/tango/internal/tgbdiff"
)

// wideGraph builds a graph of n targets spread over nested packages, with an
// optional extra target whose name sorts before every other name.
//
// The extra target is the interesting part. Dictionaries are globally sorted,
// so inserting a name that sorts first shifts every other nameID by one. A
// block digest computed over dictionary IDs would therefore change for every
// block in the graph. A digest over label bytes is unaffected.
func wideGraph(n int, extra bool) *tgb.Graph {
	rtMap := map[int32]string{1: "go_library", 2: "source file"}
	var targets []entity.OptimizedTarget
	idMap := map[int32]string{}
	var id int32
	add := func(label string) {
		id++
		idMap[id] = label
		targets = append(targets, entity.OptimizedTarget{
			ID:       id,
			RuleType: 1,
			Hash:     labelHash(label),
		})
	}
	if extra {
		// "aaa" sorts before every "target%04d" name below.
		add("//src/pkg0000:aaa")
	}
	for i := 0; i < n; i++ {
		add(fmt.Sprintf("//src/pkg%04d:target%04d", i/5, i))
	}
	return &tgb.Graph{
		Targets: targets,
		Metadata: entity.Metadata{
			TargetIDMapping:             idMap,
			RuleTypeMapping:             rtMap,
			TagMapping:                  map[int32]string{},
			AttributeNameMapping:        map[int32]string{},
			AttributeStringValueMapping: map[int32]string{},
		},
	}
}

// TestDigestSurvivesDictionaryShift is the regression test for the digest
// design: adding one early-sorting target must perturb only the block that
// contains it, not every block downstream of it.
//
// With a digest over dictionary IDs this test fails loudly — every nameID
// shifts, so no block matches and phase 0 degrades to a full-graph label
// merge-join.
func TestDigestSurvivesDictionaryShift(t *testing.T) {
	const n = 2000
	before := wideGraph(n, false)
	after := wideGraph(n, true)

	bReader := encodeGraph(t, before, 64)
	aReader := encodeGraph(t, after, 64)

	_, _, cnt, err := tgbdiff.CompareInstrumented(
		context.Background(), bReader, aReader, compareOpts())
	if err != nil {
		t.Fatalf("CompareInstrumented: %v", err)
	}

	totalBlocks := cnt.BlocksSkipped + cnt.BlocksDecoded
	if totalBlocks == 0 {
		t.Fatal("no blocks; test graph is too small to be meaningful")
	}

	// One insertion should disturb a small, constant number of blocks: the one
	// holding the new label, and its counterpart on the other side.
	if cnt.BlocksDecoded > 4 {
		t.Errorf("one inserted target invalidated %d of %d blocks; "+
			"expected <= 4. Block digests are not dictionary-independent.",
			cnt.BlocksDecoded, totalBlocks)
	}

	// And the labels actually materialised should be a handful, not the graph.
	if cnt.NodesLabelDecoded > n/4 {
		t.Errorf("decoded %d labels for a 1-target insertion into %d nodes; "+
			"phase 0 fell back to a full merge-join", cnt.NodesLabelDecoded, n)
	}

	t.Logf("blocksSkipped=%d blocksDecoded=%d labelsDecoded=%d",
		cnt.BlocksSkipped, cnt.BlocksDecoded, cnt.NodesLabelDecoded)
}

// TestDigestIgnoresNonLabelChanges checks the other half of the contract: a
// change to deps/attrs that leaves labels alone should not stop blocks from
// matching in phase 0. Phase 1 still catches the change via the hash.
func TestDigestIgnoresNonLabelChanges(t *testing.T) {
	const n = 2000
	before := wideGraph(n, false)
	after := wideGraph(n, false)

	// Rewrite one node's deps and hash, leaving every label untouched.
	after.Targets[500].DirectDependencies = []int32{1, 2, 3}
	after.Targets[500].Hash = labelHash("perturbed")

	bReader := encodeGraph(t, before, 64)
	aReader := encodeGraph(t, after, 64)

	res, _, cnt, err := tgbdiff.CompareInstrumented(
		context.Background(), bReader, aReader, compareOpts())
	if err != nil {
		t.Fatalf("CompareInstrumented: %v", err)
	}

	if cnt.BlocksDecoded != 0 {
		t.Errorf("a dep-only change invalidated %d blocks; labels were "+
			"unchanged so every block should have matched by digest",
			cnt.BlocksDecoded)
	}
	if len(res.Changed) == 0 {
		t.Error("dep change was not detected at all")
	}
	t.Logf("blocksSkipped=%d blocksDecoded=%d changed=%d",
		cnt.BlocksSkipped, cnt.BlocksDecoded, len(res.Changed))
}
