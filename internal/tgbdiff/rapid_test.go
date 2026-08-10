// Copyright (c) 2026 Uber Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tgbdiff_test

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/targetdiff"
	"github.com/uber/tango/internal/tgb"
	"github.com/uber/tango/internal/tgbdiff"
	"pgregory.net/rapid"
)

// rapidGraph draws an arbitrary well-formed graph: unique labels (including
// nested packages, where one package path is a prefix of another — the shape
// behind nested_pkg_repro_test.go), backward-only dependency edges, hashes
// that are real 40-hex digests, the all-zero digest, or the empty-hash cycle
// sentinel, plus tags, attributes, and root/external flags.
func rapidGraph(t *rapid.T, prefix string) *tgb.Graph {
	n := rapid.IntRange(1, 100).Draw(t, prefix+"N")
	numPkgs := rapid.IntRange(1, n).Draw(t, prefix+"Pkgs")

	pkgs := make([]string, numPkgs)
	for i := range pkgs {
		pkgs[i] = fmt.Sprintf("//src/p%03d", i)
		// Sometimes make this package a child of the previous one, so the
		// pool contains prefix-related package paths.
		if i > 0 && rapid.Bool().Draw(t, fmt.Sprintf("%snest%d", prefix, i)) {
			pkgs[i] = pkgs[i-1] + "/sub"
		}
	}

	targets := make([]entity.OptimizedTarget, n)
	idMap := make(map[int32]string, n)
	for i := 0; i < n; i++ {
		id := int32(i + 1)
		pkg := pkgs[rapid.IntRange(0, numPkgs-1).Draw(t, fmt.Sprintf("%spkg%d", prefix, i))]
		idMap[id] = fmt.Sprintf("%s:t%03d", pkg, i)

		var deps []int32
		if i > 0 {
			nd := rapid.IntRange(0, min(i, 6)).Draw(t, fmt.Sprintf("%snd%d", prefix, i))
			for j := 0; j < nd; j++ {
				deps = append(deps, int32(rapid.IntRange(0, i-1).Draw(t, fmt.Sprintf("%sd%d_%d", prefix, i, j))+1))
			}
			deps = dedupInt32(deps)
		}

		targets[i] = entity.OptimizedTarget{
			ID:                 id,
			Hash:               rapidHash(t, fmt.Sprintf("%sh%d", prefix, i)),
			DirectDependencies: deps,
			RuleType:           int32(rapid.IntRange(1, 2).Draw(t, fmt.Sprintf("%srt%d", prefix, i))),
			Tags:               rapidSubset(t, fmt.Sprintf("%stags%d", prefix, i)),
			Root:               rapid.Bool().Draw(t, fmt.Sprintf("%sroot%d", prefix, i)),
			External:           rapid.Bool().Draw(t, fmt.Sprintf("%sext%d", prefix, i)),
			Attributes:         rapidAttrs(t, fmt.Sprintf("%sattrs%d", prefix, i)),
		}
	}

	return &tgb.Graph{
		Targets: targets,
		Metadata: entity.Metadata{
			TargetIDMapping:             idMap,
			RuleTypeMapping:             map[int32]string{1: "go_library", 2: "source file"},
			TagMapping:                  map[int32]string{1: "manual", 2: "no-remote"},
			AttributeNameMapping:        map[int32]string{1: "visibility", 2: "testonly"},
			AttributeStringValueMapping: map[int32]string{1: "//visibility:public", 2: "True"},
		},
	}
}

// rapidHash draws one of the three hash shapes the producer can emit: a
// normal digest, the all-zero digest, or the empty-hash cycle sentinel.
func rapidHash(t *rapid.T, label string) string {
	switch rapid.IntRange(0, 11).Draw(t, label) {
	case 0:
		return "" // cycle sentinel
	case 1:
		return "0000000000000000000000000000000000000000"
	default:
		return strideTestHash(rapid.IntRange(0, 1_000_000).Draw(t, label+"seed"))
	}
}

func rapidSubset(t *rapid.T, label string) []int32 {
	var out []int32
	for _, id := range []int32{1, 2} {
		if rapid.Bool().Draw(t, fmt.Sprintf("%s_%d", label, id)) {
			out = append(out, id)
		}
	}
	return out
}

func rapidAttrs(t *rapid.T, label string) map[int32]int32 {
	out := map[int32]int32{}
	for _, name := range []int32{1, 2} {
		if rapid.Bool().Draw(t, fmt.Sprintf("%s_%d", label, name)) {
			out[name] = int32(rapid.IntRange(1, 2).Draw(t, fmt.Sprintf("%s_%dv", label, name)))
		}
	}
	return out
}

// rapidMutate draws a sequence of edits against a copy of base: hash flips,
// target deletions (with dependent edges stripped, but the lingering ID
// mapping kept — the incumbent tolerates stale mapping entries), new targets,
// dependency additions, and tag/attribute/flag changes.
func rapidMutate(t *rapid.T, base *tgb.Graph) *tgb.Graph {
	g := deepCopyGraph(base)
	nops := rapid.IntRange(0, 10).Draw(t, "nops")
	for op := 0; op < nops; op++ {
		if len(g.Targets) == 0 {
			break
		}
		i := rapid.IntRange(0, len(g.Targets)-1).Draw(t, fmt.Sprintf("op%dtarget", op))
		switch rapid.IntRange(0, 6).Draw(t, fmt.Sprintf("op%dkind", op)) {
		case 0: // flip a hash
			g.Targets[i].Hash = rapidHash(t, fmt.Sprintf("op%dhash", op))
		case 1: // delete a target and every edge into it
			deletedID := g.Targets[i].ID
			g.Targets = append(g.Targets[:i], g.Targets[i+1:]...)
			for j := range g.Targets {
				deps := g.Targets[j].DirectDependencies
				for k := 0; k < len(deps); {
					if deps[k] == deletedID {
						deps = append(deps[:k], deps[k+1:]...)
					} else {
						k++
					}
				}
				g.Targets[j].DirectDependencies = deps
			}
		case 2: // add a target depending on an existing one
			newID := int32(10_000 + op)
			if _, exists := g.Metadata.TargetIDMapping[newID]; exists {
				continue
			}
			g.Metadata.TargetIDMapping[newID] = fmt.Sprintf("//src/added:t%d", newID)
			g.Targets = append(g.Targets, entity.OptimizedTarget{
				ID:                 newID,
				Hash:               rapidHash(t, fmt.Sprintf("op%dnewhash", op)),
				RuleType:           1,
				DirectDependencies: []int32{g.Targets[i].ID},
			})
		case 3: // add a dependency edge
			j := rapid.IntRange(0, len(g.Targets)-1).Draw(t, fmt.Sprintf("op%ddep", op))
			if i != j {
				g.Targets[i].DirectDependencies = dedupInt32(
					append(g.Targets[i].DirectDependencies, g.Targets[j].ID))
			}
		case 4: // rewrite tags
			g.Targets[i].Tags = rapidSubset(t, fmt.Sprintf("op%dtags", op))
		case 5: // rewrite attributes
			g.Targets[i].Attributes = rapidAttrs(t, fmt.Sprintf("op%dattrs", op))
		case 6: // flip flags and rule type
			g.Targets[i].Root = !g.Targets[i].Root
			g.Targets[i].External = !g.Targets[i].External
			g.Targets[i].RuleType = 3 - g.Targets[i].RuleType
		}
	}
	return g
}

// TestRapidDifferential is the property-based differential for the whole TGB
// path: for arbitrary generated before/after graph pairs, arbitrary seed-attr
// allowlists, and arbitrary MaxDistance limits, running EncodeChunks +
// tgbdiff.Compare + Materialize must produce exactly what the incumbent
// pipeline (resolve to a semantic graph + targetdiff.Compare) produces,
// modulo dependency/tag order (see normalizeChanged). Case 3 in rapidMutate
// can add an edge in either direction, so cyclic graphs are exercised too.
func TestRapidDifferential(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		before := rapidGraph(t, "b")
		after := rapidMutate(t, before)
		seedAttrs := rapid.SampledFrom([]map[string]bool{
			nil,
			{"visibility": true},
			{"visibility": true, "testonly": true},
			{"nonexistent-attr": true},
		}).Draw(t, "seedAttrs")
		maxDistance := rapid.SampledFrom([]int32{-1, 0, 1, 3}).Draw(t, "maxDistance")

		incumbent, err := targetdiff.Compare(context.Background(), targetdiff.Request{
			Before:      toOracleGraph(before, seedAttrs),
			After:       toOracleGraph(after, seedAttrs),
			MaxDistance: maxDistance,
		})
		if err != nil {
			t.Fatalf("targetdiff.Compare: %v", err)
		}

		bReader := rapidEncode(t, before)
		aReader := rapidEncode(t, after)
		res, err := tgbdiff.Compare(context.Background(), bReader, aReader,
			tgbdiff.Options{MaxDistance: maxDistance, SeedAttrs: seedAttrs})
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
			t.Fatalf("incumbent %d changed, TGB path %d changed\n  incumbent: %v\n  tgb:       %v",
				len(wantN), len(gotN), changedLabels(wantN), changedLabels(gotN))
		}
		for i := range wantN {
			if !reflect.DeepEqual(wantN[i], gotN[i]) {
				t.Fatalf("changed[%d] differs:\n  incumbent: %s\n  tgb:       %s",
					i, dumpChanged(wantN[i]), dumpChanged(gotN[i]))
			}
		}
	})
}

// rapidEncode runs a graph through the chunked production entry point with a
// drawn chunk size, so chunk-boundary placement is part of the search space.
func rapidEncode(t *rapid.T, g *tgb.Graph) *tgb.Reader {
	chunkSize := rapid.IntRange(1, len(g.Targets)+1).Draw(t, "chunkSize")
	var buf bytes.Buffer
	if err := tgbdiff.EncodeChunks(graphToChunks(g, chunkSize), &buf); err != nil {
		t.Fatalf("EncodeChunks: %v", err)
	}
	r, err := tgb.NewReader(buf.Bytes())
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return r
}

func changedLabels(cts []*targetdiff.ChangedTarget) []string {
	out := make([]string, 0, len(cts))
	for _, ct := range cts {
		name := ""
		if ct.After != nil {
			name = ct.After.Name
		} else if ct.Before != nil {
			name = ct.Before.Name
		}
		out = append(out, fmt.Sprintf("%s(type=%d,dist=%d)", name, ct.ChangeType, ct.Distance))
	}
	sort.Strings(out)
	return out
}
