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

package tgbdiff

import (
	"fmt"
	"io"
	"sort"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/targetdiff"
	"github.com/uber/tango/internal/tgb"
)

// Production encode parameters. Hash stride 20 stores the full SHA-1, making
// the blob a lossless representation of what the producer streamed (there is
// no truncation anywhere in the TGB path). Block size 512 bounds how much
// label decoding one structural change costs a comparison; see the codec's
// EncodeOptions for the trade-off.
const (
	encodeHashBytes = 20
	encodeBlockSize = 512
)

// MergeChunks merges a chunked GetTargetGraphResponse stream into a single
// tgb.Graph, with the same semantics as the controller's
// getTargetsAndMetadata: targets are deduplicated by ID (a later chunk's
// target replaces an earlier one's) and metadata maps are merged across
// chunks.
func MergeChunks(chunks []entity.GetTargetGraphResponse) *tgb.Graph {
	g := &tgb.Graph{
		Metadata: entity.Metadata{
			TargetIDMapping:             make(map[int32]string),
			RuleTypeMapping:             make(map[int32]string),
			TagMapping:                  make(map[int32]string),
			AttributeNameMapping:        make(map[int32]string),
			AttributeStringValueMapping: make(map[int32]string),
		},
	}
	// byID mirrors the incumbent's map [ID]*target: last write wins. The
	// slice keeps first-seen order for determinism; the encoder re-sorts by
	// label anyway.
	byID := make(map[int32]int)
	for _, chunk := range chunks {
		for i := range chunk.Targets {
			t := chunk.Targets[i]
			if idx, ok := byID[t.ID]; ok {
				g.Targets[idx] = t
				continue
			}
			byID[t.ID] = len(g.Targets)
			g.Targets = append(g.Targets, t)
		}
		if m := chunk.Metadata; m != nil {
			for k, v := range m.TargetIDMapping {
				g.Metadata.TargetIDMapping[k] = v
			}
			for k, v := range m.RuleTypeMapping {
				g.Metadata.RuleTypeMapping[k] = v
			}
			for k, v := range m.TagMapping {
				g.Metadata.TagMapping[k] = v
			}
			for k, v := range m.AttributeNameMapping {
				g.Metadata.AttributeNameMapping[k] = v
			}
			for k, v := range m.AttributeStringValueMapping {
				g.Metadata.AttributeStringValueMapping[k] = v
			}
			for k, v := range m.AllTargetsFileHashes {
				if g.Metadata.AllTargetsFileHashes == nil {
					g.Metadata.AllTargetsFileHashes = make(map[string]string, len(m.AllTargetsFileHashes))
				}
				g.Metadata.AllTargetsFileHashes[k] = v
			}
		}
	}
	return g
}

// EncodeChunks merges a chunked target graph stream and writes it to w as a
// TGB blob at the production parameters (20-byte hashes, 512-node blocks).
// Encoding is strict: a target without a label mapping or with a malformed
// hash is an error, not a silent drop — a blob that encodes must decode to
// exactly what was streamed.
func EncodeChunks(chunks []entity.GetTargetGraphResponse, w io.Writer) error {
	return tgb.Encode(w, MergeChunks(chunks), tgb.EncodeOptions{
		HashBytes: encodeHashBytes,
		BlockSize: encodeBlockSize,
	})
}

// Materialize converts a label-keyed comparison Result into the full
// targetdiff.Result shape the response pipeline consumes, building Before and
// After targets for changed nodes only. This pays label resolution
// proportional to changed nodes × average degree — the cost of the response
// schema, paid only for the output set, where the incumbent path pays it for
// the whole graph up front.
//
// seedAttrs mirrors the allowlist the incumbent path applies in toDiffGraph:
// when non-nil, only allowlisted attribute names appear on the materialized
// targets, matching the incumbent's response byte for byte. Pass the same
// value that was passed to Compare.
func Materialize(before, after *tgb.Reader, result *Result, seedAttrs map[string]bool) (targetdiff.Result, error) {
	out := targetdiff.Result{
		ChangedTargets: make([]*targetdiff.ChangedTarget, 0, len(result.Changed)),
	}
	for _, c := range result.Changed {
		ct := &targetdiff.ChangedTarget{
			ChangeType: c.ChangeType,
			Distance:   c.Distance,
		}
		pkg, name := tgb.SplitLabelString(c.Label)
		if c.ChangeType == targetdiff.ChangeTypeNew || c.ChangeType == targetdiff.ChangeTypeChanged {
			node := after.FindNode(pkg, name)
			if node < 0 {
				return targetdiff.Result{}, fmt.Errorf("tgbdiff: changed label %q not found in after graph", c.Label)
			}
			ct.After = materializeTarget(after, node, seedAttrs)
		}
		if c.ChangeType == targetdiff.ChangeTypeDeleted || c.ChangeType == targetdiff.ChangeTypeChanged {
			node := before.FindNode(pkg, name)
			if node < 0 {
				return targetdiff.Result{}, fmt.Errorf("tgbdiff: changed label %q not found in before graph", c.Label)
			}
			ct.Before = materializeTarget(before, node, seedAttrs)
		}
		out.ChangedTargets = append(out.ChangedTargets, ct)
	}
	return out, nil
}

// SemanticGraph materialises every node of a TGB blob into the label-keyed
// targetdiff.Graph shape, with the same per-target field semantics as
// Materialize. This is the shadow-compare oracle's input builder: it lets
// targetdiff.Compare run over a TGB blob without any gob-era decode path.
// It walks the whole graph — never use it on the serving path.
func SemanticGraph(r *tgb.Reader, seedAttrs map[string]bool) targetdiff.Graph {
	n := r.NodeCount()
	g := make(targetdiff.Graph, n)
	for i := 0; i < n; i++ {
		t := materializeTarget(r, i, seedAttrs)
		g[t.Name] = t
	}
	return g
}

// ResultsEquivalent reports whether two comparison results agree, treating
// each target's Dependencies and Tags as sets — the one field where the TGB
// and incumbent paths legitimately differ (the incumbent preserves the
// producer's stream order, TGB stores dependencies in sorted node order).
// Everything else must match exactly. On disagreement it returns a
// description of the first divergence for logging. Both inputs are treated
// as read-only; comparison works on sorted copies.
func ResultsEquivalent(a, b targetdiff.Result) (bool, string) {
	an := normalizeResult(a)
	bn := normalizeResult(b)
	if len(an) != len(bn) {
		return false, fmt.Sprintf("changed-target counts differ: %d vs %d", len(an), len(bn))
	}
	for i := range an {
		if !changedTargetsEqual(an[i], bn[i]) {
			return false, fmt.Sprintf("changed target %q differs: %+v vs %+v",
				changedLabel(an[i]), an[i], bn[i])
		}
	}
	return true, ""
}

// normalizeResult returns the changed targets sorted by label, with each
// target's Dependencies and Tags sorted, copying every slice it reorders.
func normalizeResult(res targetdiff.Result) []*targetdiff.ChangedTarget {
	sortedTarget := func(t *targetdiff.Target) *targetdiff.Target {
		if t == nil {
			return nil
		}
		c := *t
		if len(c.Dependencies) > 0 {
			c.Dependencies = append([]string(nil), c.Dependencies...)
			sort.Strings(c.Dependencies)
		}
		if len(c.Tags) > 0 {
			c.Tags = append([]string(nil), c.Tags...)
			sort.Strings(c.Tags)
		}
		return &c
	}
	out := make([]*targetdiff.ChangedTarget, 0, len(res.ChangedTargets))
	for _, ct := range res.ChangedTargets {
		out = append(out, &targetdiff.ChangedTarget{
			ChangeType: ct.ChangeType,
			Distance:   ct.Distance,
			Before:     sortedTarget(ct.Before),
			After:      sortedTarget(ct.After),
		})
	}
	sort.Slice(out, func(i, j int) bool { return changedLabel(out[i]) < changedLabel(out[j]) })
	return out
}

func changedLabel(ct *targetdiff.ChangedTarget) string {
	if ct.After != nil {
		return ct.After.Name
	}
	if ct.Before != nil {
		return ct.Before.Name
	}
	return ""
}

func changedTargetsEqual(a, b *targetdiff.ChangedTarget) bool {
	return a.ChangeType == b.ChangeType &&
		a.Distance == b.Distance &&
		targetsEqual(a.Before, b.Before) &&
		targetsEqual(a.After, b.After)
}

func targetsEqual(a, b *targetdiff.Target) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if a == nil {
		return true
	}
	return a.Name == b.Name &&
		a.Hash == b.Hash &&
		a.RuleType == b.RuleType &&
		a.Root == b.Root &&
		a.External == b.External &&
		slicesEqual(a.Dependencies, b.Dependencies) &&
		slicesEqual(a.Tags, b.Tags) &&
		mapsEqual(a.Attributes, b.Attributes)
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// materializeTarget builds the semantic target for one node, resolving
// dependency and tag IDs to strings from the node's own revision, with the
// same field semantics as the controller's toDiffGraph.
func materializeTarget(r *tgb.Reader, node int, seedAttrs map[string]bool) *targetdiff.Target {
	t := &targetdiff.Target{
		Name:     r.Label(node),
		Hash:     r.Hash(node),
		RuleType: r.RuleType(node),
		Root:     r.Root(node),
		External: r.External(node),
	}
	if deps := r.Deps(node, nil); len(deps) > 0 {
		t.Dependencies = make([]string, 0, len(deps))
		for _, d := range deps {
			t.Dependencies = append(t.Dependencies, r.Label(int(d)))
		}
	}
	if tags := r.Tags(node, nil); len(tags) > 0 {
		t.Tags = make([]string, 0, len(tags))
		for _, id := range tags {
			if name := r.TagName(id); name != "" {
				t.Tags = append(t.Tags, name)
			}
		}
	}
	if attrs := r.Attrs(node); len(attrs) > 0 {
		t.Attributes = make(map[string]string, len(attrs))
		for k, v := range attrs {
			if seedAttrs != nil && !seedAttrs[k] {
				continue
			}
			t.Attributes[k] = v
		}
	}
	return t
}
