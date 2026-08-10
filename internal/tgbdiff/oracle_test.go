package tgbdiff_test

import (
	"context"
	"sort"
	"testing"

	"github.com/uber/tango/internal/targetdiff"
	"github.com/uber/tango/internal/tgb"
	"github.com/uber/tango/internal/tgbdiff"
)

// This file is the oracle side of the differential suite: every tgbdiff test
// that asserts comparison output checks it against tango's production
// algorithm, internal/targetdiff, run over the same graphs. The conversion
// below must mirror the controller's toDiffGraph exactly (including the
// seedAttrs allowlist and its skip/drop rules for unresolvable IDs) — it is
// the same semantic boundary the production TGB path will cross.

// toOracleGraph resolves a tgb.Graph's int32 IDs into a semantic
// targetdiff.Graph keyed by canonical target name, mirroring
// controller/getchangedtargets.go toDiffGraph: targets with no name mapping
// are skipped; dependency and tag IDs that don't resolve are dropped;
// attributes with unresolvable names are dropped; when seedAttrs is non-nil,
// only allowlisted attribute names are kept.
func toOracleGraph(g *tgb.Graph, seedAttrs map[string]bool) targetdiff.Graph {
	meta := g.Metadata
	graph := make(targetdiff.Graph, len(g.Targets))
	for i := range g.Targets {
		t := &g.Targets[i]
		name := meta.TargetIDMapping[t.ID]
		if name == "" {
			continue
		}
		target := &targetdiff.Target{
			Name:     name,
			Hash:     t.Hash,
			RuleType: meta.RuleTypeMapping[t.RuleType],
			Root:     t.Root,
			External: t.External,
		}
		if deps := t.DirectDependencies; len(deps) > 0 {
			target.Dependencies = make([]string, 0, len(deps))
			for _, depID := range deps {
				if depName := meta.TargetIDMapping[depID]; depName != "" {
					target.Dependencies = append(target.Dependencies, depName)
				}
			}
		}
		if tags := t.Tags; len(tags) > 0 {
			target.Tags = make([]string, 0, len(tags))
			for _, tagID := range tags {
				if tagName := meta.TagMapping[tagID]; tagName != "" {
					target.Tags = append(target.Tags, tagName)
				}
			}
		}
		if attrs := t.Attributes; len(attrs) > 0 {
			target.Attributes = make(map[string]string, len(attrs))
			for nameID, valID := range attrs {
				attrName := meta.AttributeNameMapping[nameID]
				if attrName == "" {
					continue
				}
				if seedAttrs != nil && !seedAttrs[attrName] {
					continue
				}
				target.Attributes[attrName] = meta.AttributeStringValueMapping[valID]
			}
		}
		graph[name] = target
	}
	return graph
}

// oracleCompare runs internal/targetdiff over the two graphs and normalises
// its result into tgbdiff's label-keyed, label-sorted shape so the two
// implementations can be compared with reflect.DeepEqual.
//
// Seed is derived as Distance == 0: in both implementations exactly the seed
// set is assigned distance 0 (BFS-discovered nodes get >= 1, unreachable get
// -1), so the flag round-trips through targetdiff's result even though
// targetdiff does not expose it directly.
func oracleCompare(t *testing.T, before, after *tgb.Graph, seedAttrs map[string]bool, maxDistance int32) *tgbdiff.Result {
	t.Helper()
	result, err := targetdiff.Compare(context.Background(), targetdiff.Request{
		Before:      toOracleGraph(before, seedAttrs),
		After:       toOracleGraph(after, seedAttrs),
		MaxDistance: maxDistance,
	})
	if err != nil {
		t.Fatalf("targetdiff.Compare (oracle): %v", err)
	}

	changed := make([]tgbdiff.ChangedTarget, 0, len(result.ChangedTargets))
	for _, ct := range result.ChangedTargets {
		label := ""
		switch {
		case ct.After != nil:
			label = ct.After.Name
		case ct.Before != nil:
			label = ct.Before.Name
		}
		changed = append(changed, tgbdiff.ChangedTarget{
			Label:      label,
			ChangeType: ct.ChangeType,
			Distance:   ct.Distance,
			Seed:       ct.Distance == 0,
		})
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].Label < changed[j].Label })
	return &tgbdiff.Result{Changed: changed}
}
