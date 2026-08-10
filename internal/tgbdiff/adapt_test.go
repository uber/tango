package tgbdiff_test

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/targetdiff"
	"github.com/uber/tango/internal/tgb"
	"github.com/uber/tango/internal/tgbdiff"
)

// graphToChunks splits a graph into a chunked GetTargetGraphResponse stream
// the way the producer does: target batches of chunkSize, metadata in its own
// chunks — deliberately split across two chunks to exercise metadata merging.
func graphToChunks(g *tgb.Graph, chunkSize int) []entity.GetTargetGraphResponse {
	var chunks []entity.GetTargetGraphResponse
	for lo := 0; lo < len(g.Targets); lo += chunkSize {
		hi := lo + chunkSize
		if hi > len(g.Targets) {
			hi = len(g.Targets)
		}
		chunks = append(chunks, entity.GetTargetGraphResponse{
			Targets: append([]entity.OptimizedTarget(nil), g.Targets[lo:hi]...),
		})
	}
	// Split the target-ID mapping in half across two metadata chunks, like a
	// producer whose target_id_mapping exceeded the message size limit.
	half := make(map[int32]string)
	rest := make(map[int32]string)
	i := 0
	for k, v := range g.Metadata.TargetIDMapping {
		if i%2 == 0 {
			half[k] = v
		} else {
			rest[k] = v
		}
		i++
	}
	chunks = append(chunks,
		entity.GetTargetGraphResponse{Metadata: &entity.Metadata{
			TargetIDMapping: half,
			RuleTypeMapping: g.Metadata.RuleTypeMapping,
		}},
		entity.GetTargetGraphResponse{Metadata: &entity.Metadata{
			TargetIDMapping:             rest,
			TagMapping:                  g.Metadata.TagMapping,
			AttributeNameMapping:        g.Metadata.AttributeNameMapping,
			AttributeStringValueMapping: g.Metadata.AttributeStringValueMapping,
		}},
	)
	return chunks
}

// TestMergeChunks pins the incumbent's merge semantics: metadata merged
// across chunks, targets deduplicated by ID with last-write-wins.
func TestMergeChunks(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	g := syntheticGraph(rng, 50, 0.3)
	chunks := graphToChunks(g, 7)

	// Duplicate one target in a later chunk with a different hash: the later
	// copy must win, as it does in getTargetsAndMetadata's ID map.
	dup := g.Targets[10]
	dup.Hash = strideTestHash(1234)
	chunks = append(chunks, entity.GetTargetGraphResponse{
		Targets: []entity.OptimizedTarget{dup},
	})

	merged := tgbdiff.MergeChunks(chunks)
	if len(merged.Targets) != len(g.Targets) {
		t.Fatalf("merged %d targets, want %d", len(merged.Targets), len(g.Targets))
	}
	found := false
	for _, mt := range merged.Targets {
		if mt.ID == dup.ID {
			found = true
			if mt.Hash != dup.Hash {
				t.Errorf("duplicated target ID %d: hash %q, want later chunk's %q", dup.ID, mt.Hash, dup.Hash)
			}
		}
	}
	if !found {
		t.Fatal("duplicated target missing from merge")
	}
	if !reflect.DeepEqual(merged.Metadata, g.Metadata) {
		t.Error("merged metadata differs from original")
	}
}

// encodeChunked runs a graph through the chunked production entry point and
// returns a Reader over the resulting blob.
func encodeChunked(t *testing.T, g *tgb.Graph) *tgb.Reader {
	t.Helper()
	var buf bytes.Buffer
	if err := tgbdiff.EncodeChunks(graphToChunks(g, 13), &buf); err != nil {
		t.Fatalf("EncodeChunks: %v", err)
	}
	r, err := tgb.NewReader(buf.Bytes())
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return r
}

// normalizeChanged canonicalises a targetdiff result for comparison: sorted
// by label, with Dependencies and Tags sorted within each target. Dependency
// and tag ORDER is the one place the two paths legitimately differ — the
// incumbent preserves the producer's stream order, TGB stores dependencies in
// sorted node order — so equality is set equality on those fields. Everything
// else must match exactly.
func normalizeChanged(res targetdiff.Result) []*targetdiff.ChangedTarget {
	norm := func(tg *targetdiff.Target) {
		if tg == nil {
			return
		}
		sort.Strings(tg.Dependencies)
		sort.Strings(tg.Tags)
	}
	out := append([]*targetdiff.ChangedTarget(nil), res.ChangedTargets...)
	for _, ct := range out {
		norm(ct.Before)
		norm(ct.After)
	}
	sort.Slice(out, func(i, j int) bool {
		labelOf := func(ct *targetdiff.ChangedTarget) string {
			if ct.After != nil {
				return ct.After.Name
			}
			if ct.Before != nil {
				return ct.Before.Name
			}
			return ""
		}
		return labelOf(out[i]) < labelOf(out[j])
	})
	return out
}

// TestMaterializeMatchesIncumbent is the full end-to-end differential: the
// same chunked streams go through (a) the incumbent pipeline — merge, resolve
// to a semantic graph, targetdiff.Compare — and (b) the TGB pipeline —
// EncodeChunks, tgbdiff.Compare, Materialize. The materialized results must
// be identical (modulo dependency/tag order; see normalizeChanged).
func TestMaterializeMatchesIncumbent(t *testing.T) {
	rng := rand.New(rand.NewSource(17))
	for _, seedAttrs := range []map[string]bool{
		nil,
		{"visibility": true},
		{"nonexistent-attr": true},
	} {
		base := syntheticGraph(rng, 400, 0.4)

		// Mutate: perturb a couple of hashes, delete one target, add one.
		after := deepCopyGraph(base)
		after.Targets[31].Hash = perturbHash(after.Targets[31].Hash)
		after.Targets[200].Hash = perturbHash(after.Targets[200].Hash)
		deletedID := after.Targets[97].ID
		after.Targets = append(after.Targets[:97], after.Targets[98:]...)
		// Deleting a target means its dependents' dep lists changed too — a
		// real producer re-emits those edges without the dead ID. (A dep list
		// still referencing a nonexistent target is a producer bug, and the
		// encoder rejects it; TestEncodeChunksRejectsDanglingDep pins that.)
		for i := range after.Targets {
			deps := after.Targets[i].DirectDependencies
			for j := 0; j < len(deps); {
				if deps[j] == deletedID {
					deps = append(deps[:j], deps[j+1:]...)
				} else {
					j++
				}
			}
			after.Targets[i].DirectDependencies = deps
		}
		newID := int32(10_000)
		after.Metadata.TargetIDMapping[newID] = "//src/pkg0000:aaa_new_target"
		after.Targets = append(after.Targets, entity.OptimizedTarget{
			ID: newID, Hash: strideTestHash(77), RuleType: 1,
		})
		recomputeHashes(after)

		// (a) incumbent path.
		incumbent, err := targetdiff.Compare(context.Background(), targetdiff.Request{
			Before:      toOracleGraph(base, seedAttrs),
			After:       toOracleGraph(after, seedAttrs),
			MaxDistance: -1,
		})
		if err != nil {
			t.Fatalf("targetdiff.Compare: %v", err)
		}

		// (b) TGB path, from the same chunked streams.
		bReader := encodeChunked(t, base)
		aReader := encodeChunked(t, after)
		res, err := tgbdiff.Compare(context.Background(), bReader, aReader,
			tgbdiff.Options{MaxDistance: -1, SeedAttrs: seedAttrs})
		if err != nil {
			t.Fatalf("tgbdiff.Compare: %v", err)
		}
		got, err := tgbdiff.Materialize(bReader, aReader, res, seedAttrs)
		if err != nil {
			t.Fatalf("Materialize: %v", err)
		}

		wantN := normalizeChanged(incumbent)
		gotN := normalizeChanged(got)
		if len(wantN) != len(gotN) {
			t.Fatalf("seedAttrs=%v: incumbent %d changed, TGB path %d changed",
				seedAttrs, len(wantN), len(gotN))
		}
		for i := range wantN {
			if !reflect.DeepEqual(wantN[i], gotN[i]) {
				t.Errorf("seedAttrs=%v: changed[%d] differs:\n  incumbent: %s\n  tgb:       %s",
					seedAttrs, i, dumpChanged(wantN[i]), dumpChanged(gotN[i]))
			}
		}
	}
}

// TestEncodeChunksRejectsDanglingDep pins the encoder's strictness: a
// dependency ID with no target in the graph is an error, not a silent drop.
// The incumbent decode path keeps such a dep as a label whenever its name
// mapping still exists, so dropping the edge would change comparison results
// (a shrunk dep set reads as a structural change). Encoding must be lossless
// or fail.
func TestEncodeChunksRejectsDanglingDep(t *testing.T) {
	rng := rand.New(rand.NewSource(23))
	g := syntheticGraph(rng, 20, 0.3)
	g.Targets[5].DirectDependencies = append(g.Targets[5].DirectDependencies, 99_999)

	var buf bytes.Buffer
	if err := tgbdiff.EncodeChunks(graphToChunks(g, 7), &buf); err == nil {
		t.Fatal("EncodeChunks accepted a dependency ID with no target; want error")
	}
}

// TestSemanticGraphMatchesOracle: SemanticGraph (the shadow oracle's input
// builder) must produce the same semantic graph as toDiffGraph does from the
// raw entities, modulo dependency/tag order.
func TestSemanticGraphMatchesOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(29))
	for _, seedAttrs := range []map[string]bool{nil, {"visibility": true}} {
		g := syntheticGraph(rng, 200, 0.4)
		r := encodeChunked(t, g)

		got := tgbdiff.SemanticGraph(r, seedAttrs)
		want := toOracleGraph(g, seedAttrs)
		if len(got) != len(want) {
			t.Fatalf("seedAttrs=%v: %d targets, oracle has %d", seedAttrs, len(got), len(want))
		}
		for name, wt := range want {
			gt, ok := got[name]
			if !ok {
				t.Fatalf("seedAttrs=%v: target %q missing", seedAttrs, name)
			}
			normTarget(gt)
			normTarget(wt)
			if !reflect.DeepEqual(gt, wt) {
				t.Errorf("seedAttrs=%v: target %q differs:\n  got:  %+v\n  want: %+v", seedAttrs, name, gt, wt)
			}
		}
	}
}

func normTarget(tg *targetdiff.Target) {
	sort.Strings(tg.Dependencies)
	sort.Strings(tg.Tags)
}

// TestResultsEquivalent pins the shadow comparison's equality contract:
// dependency and tag order is set equality, everything else exact.
func TestResultsEquivalent(t *testing.T) {
	base := func() targetdiff.Result {
		return targetdiff.Result{ChangedTargets: []*targetdiff.ChangedTarget{
			{
				ChangeType: targetdiff.ChangeTypeChanged,
				Distance:   1,
				Before:     &targetdiff.Target{Name: "//a:t", Hash: "aa", Dependencies: []string{"//a:x", "//a:y"}, Tags: []string{"manual"}},
				After:      &targetdiff.Target{Name: "//a:t", Hash: "bb", Dependencies: []string{"//a:x", "//a:y"}, Tags: []string{"manual"}},
			},
			{
				ChangeType: targetdiff.ChangeTypeNew,
				After:      &targetdiff.Target{Name: "//b:new", Hash: "cc", Attributes: map[string]string{"visibility": "public"}},
			},
		}}
	}

	if ok, why := tgbdiff.ResultsEquivalent(base(), base()); !ok {
		t.Fatalf("identical results reported divergent: %s", why)
	}

	// Permuted deps/tags and changed-target order: still equivalent.
	permuted := base()
	permuted.ChangedTargets[0].After.Dependencies = []string{"//a:y", "//a:x"}
	permuted.ChangedTargets[0], permuted.ChangedTargets[1] = permuted.ChangedTargets[1], permuted.ChangedTargets[0]
	if ok, why := tgbdiff.ResultsEquivalent(base(), permuted); !ok {
		t.Fatalf("order-permuted result reported divergent: %s", why)
	}
	// ResultsEquivalent must not have mutated its inputs.
	if permuted.ChangedTargets[1].After.Dependencies[0] != "//a:y" {
		t.Fatal("ResultsEquivalent mutated its input")
	}

	for name, mutate := range map[string]func(*targetdiff.Result){
		"distance":     func(r *targetdiff.Result) { r.ChangedTargets[0].Distance = 2 },
		"hash":         func(r *targetdiff.Result) { r.ChangedTargets[0].After.Hash = "zz" },
		"change type":  func(r *targetdiff.Result) { r.ChangedTargets[1].ChangeType = targetdiff.ChangeTypeDeleted },
		"dep set":      func(r *targetdiff.Result) { r.ChangedTargets[0].After.Dependencies = []string{"//a:x"} },
		"attribute":    func(r *targetdiff.Result) { r.ChangedTargets[1].After.Attributes["visibility"] = "private" },
		"extra target": func(r *targetdiff.Result) { r.ChangedTargets = append(r.ChangedTargets, r.ChangedTargets[0]) },
	} {
		mutated := base()
		mutate(&mutated)
		if ok, _ := tgbdiff.ResultsEquivalent(base(), mutated); ok {
			t.Errorf("%s difference not detected", name)
		}
	}
}

func dumpChanged(ct *targetdiff.ChangedTarget) string {
	dump := func(tg *targetdiff.Target) string {
		if tg == nil {
			return "<nil>"
		}
		return fmt.Sprintf("{%s hash=%s rt=%s deps=%v tags=%v attrs=%v root=%v ext=%v}",
			tg.Name, tg.Hash, tg.RuleType, tg.Dependencies, tg.Tags, tg.Attributes, tg.Root, tg.External)
	}
	return fmt.Sprintf("type=%d dist=%d before=%s after=%s",
		ct.ChangeType, ct.Distance, dump(ct.Before), dump(ct.After))
}

// BenchmarkCompareAndMaterialize measures the serving path's cost on a wide
// diff (every node's hash perturbed — the worst case for Materialize, which
// pays per changed node). Run before flipping graph_format in production to
// budget the materialization cost; see INTEGRATION_PLAN.md §5.
func BenchmarkCompareAndMaterialize(b *testing.B) {
	rng := rand.New(rand.NewSource(41))
	base := syntheticGraph(rng, 100_000, 0.3)
	after := deepCopyGraph(base)
	for i := range after.Targets {
		after.Targets[i].Hash = perturbHash(after.Targets[i].Hash)
	}

	var bBuf, aBuf bytes.Buffer
	if err := tgbdiff.EncodeChunks(graphToChunks(base, 4096), &bBuf); err != nil {
		b.Fatal(err)
	}
	if err := tgbdiff.EncodeChunks(graphToChunks(after, 4096), &aBuf); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bReader, err := tgb.NewReader(bBuf.Bytes())
		if err != nil {
			b.Fatal(err)
		}
		aReader, err := tgb.NewReader(aBuf.Bytes())
		if err != nil {
			b.Fatal(err)
		}
		res, err := tgbdiff.Compare(context.Background(), bReader, aReader, tgbdiff.Options{MaxDistance: -1})
		if err != nil {
			b.Fatal(err)
		}
		result, err := tgbdiff.Materialize(bReader, aReader, res, nil)
		if err != nil {
			b.Fatal(err)
		}
		if len(result.ChangedTargets) != len(after.Targets) {
			b.Fatalf("changed %d, want %d", len(result.ChangedTargets), len(after.Targets))
		}
	}
}
