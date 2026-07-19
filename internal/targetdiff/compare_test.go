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

package targetdiff

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompare_changedSourcePropagatesToConsumer(t *testing.T) {
	before := Graph{
		"//pkg:file.go": {Name: "//pkg:file.go", Hash: "source-old", RuleType: "source file"},
		"//pkg:lib":     {Name: "//pkg:lib", Hash: "rule-old", RuleType: "rule", Dependencies: []string{"//pkg:file.go"}},
		"//app:bin":     {Name: "//app:bin", Hash: "consumer-old", RuleType: "rule", Dependencies: []string{"//pkg:lib"}},
		"//app:outer":   {Name: "//app:outer", Hash: "outer-old", RuleType: "rule", Dependencies: []string{"//app:bin"}},
	}
	after := Graph{
		"//pkg:file.go": {Name: "//pkg:file.go", Hash: "source-new", RuleType: "source file"},
		"//pkg:lib":     {Name: "//pkg:lib", Hash: "rule-new", RuleType: "rule", Dependencies: []string{"//pkg:file.go"}},
		"//app:bin":     {Name: "//app:bin", Hash: "consumer-new", RuleType: "rule", Dependencies: []string{"//pkg:lib"}},
		"//app:outer":   {Name: "//app:outer", Hash: "outer-new", RuleType: "rule", Dependencies: []string{"//app:bin"}},
	}

	result, err := Compare(t.Context(), Request{Before: before, After: after, MaxDistance: -1})
	require.NoError(t, err)
	require.Len(t, result.ChangedTargets, 4)

	byName := changesByName(result)
	assert.Equal(t, int32(0), byName["//pkg:file.go"].Distance)
	assert.Equal(t, int32(0), byName["//pkg:lib"].Distance)
	assert.Equal(t, int32(1), byName["//app:bin"].Distance)
	assert.Equal(t, int32(2), byName["//app:outer"].Distance)

	limited, err := Compare(t.Context(), Request{Before: before, After: after, MaxDistance: 1})
	require.NoError(t, err)
	assert.Equal(t, int32(-1), changesByName(limited)["//app:outer"].Distance)
}

func TestCompare_classifiesNewChangedAndDeleted(t *testing.T) {
	before := Graph{
		"deleted": {Name: "deleted", Hash: "old"},
		"changed": {Name: "changed", Hash: "old"},
	}
	after := Graph{
		"new":     {Name: "new", Hash: "new"},
		"changed": {Name: "changed", Hash: "new"},
	}

	result, err := Compare(t.Context(), Request{Before: before, After: after, MaxDistance: -1})
	require.NoError(t, err)
	byName := changesByName(result)

	assert.Equal(t, ChangeTypeNew, byName["new"].ChangeType)
	assert.Nil(t, byName["new"].Before)
	assert.Equal(t, after["new"], byName["new"].After)
	assert.Equal(t, ChangeTypeChanged, byName["changed"].ChangeType)
	assert.Equal(t, before["changed"], byName["changed"].Before)
	assert.Equal(t, after["changed"], byName["changed"].After)
	assert.Equal(t, ChangeTypeDeleted, byName["deleted"].ChangeType)
	assert.Equal(t, before["deleted"], byName["deleted"].Before)
	assert.Nil(t, byName["deleted"].After)
}

func TestCompare_doesNotTraverseUnchangedTargets(t *testing.T) {
	before := Graph{
		"source": {Name: "source", Hash: "old", RuleType: "source file"},
		"stable": {Name: "stable", Hash: "same", Dependencies: []string{"source"}},
	}
	after := Graph{
		"source": {Name: "source", Hash: "new", RuleType: "source file"},
		"stable": {Name: "stable", Hash: "same", Dependencies: []string{"source"}},
	}

	result, err := Compare(t.Context(), Request{Before: before, After: after, MaxDistance: -1})
	require.NoError(t, err)
	require.Len(t, result.ChangedTargets, 1)
	assert.Equal(t, "source", result.ChangedTargets[0].After.Name)
}

func TestCompare_shortestDistanceWins(t *testing.T) {
	// A transitive consumer reachable from two seeds via paths of different
	// lengths must take the shorter distance. Rules that directly depend on a
	// changed source file are themselves seeds (distance 0); only rules with no
	// changed source dependency are transitive consumers.
	//   s1 (source) <- l1 <- l2 <- l3
	//   s2 (source) <- la <-------- l3
	// Seeds: s1, s2, l1, la (=0). Transitive: l2=1, l3=min(2 via l2, 1 via la)=1.
	graph := func(suffix string) Graph {
		return Graph{
			"s1": {Name: "s1", Hash: "s1-" + suffix, RuleType: "source file"},
			"s2": {Name: "s2", Hash: "s2-" + suffix, RuleType: "source file"},
			"l1": {Name: "l1", Hash: "l1-" + suffix, RuleType: "rule", Dependencies: []string{"s1"}},
			"la": {Name: "la", Hash: "la-" + suffix, RuleType: "rule", Dependencies: []string{"s2"}},
			"l2": {Name: "l2", Hash: "l2-" + suffix, RuleType: "rule", Dependencies: []string{"l1"}},
			"l3": {Name: "l3", Hash: "l3-" + suffix, RuleType: "rule", Dependencies: []string{"l2", "la"}},
		}
	}

	result, err := Compare(t.Context(), Request{Before: graph("old"), After: graph("new"), MaxDistance: -1})
	require.NoError(t, err)
	byName := changesByName(result)
	assert.Equal(t, int32(0), byName["l1"].Distance)
	assert.Equal(t, int32(0), byName["la"].Distance)
	assert.Equal(t, int32(1), byName["l2"].Distance)
	assert.Equal(t, int32(1), byName["l3"].Distance, "l3 reaches seed la at distance 1, shorter than 2 via l1→l2")
}

func TestCompare_newTargetSeededUnderMaxDistance(t *testing.T) {
	// A NEW target is a seed and keeps distance 0 even when a max distance is set.
	before := Graph{}
	after := Graph{
		"new": {Name: "new", Hash: "new"},
	}

	result, err := Compare(t.Context(), Request{Before: before, After: after, MaxDistance: 1})
	require.NoError(t, err)
	byName := changesByName(result)
	require.Contains(t, byName, "new")
	assert.Equal(t, int32(0), byName["new"].Distance)
}

func changesByName(result Result) map[string]*ChangedTarget {
	changes := make(map[string]*ChangedTarget, len(result.ChangedTargets))
	for _, changed := range result.ChangedTargets {
		if changed.After != nil {
			changes[changed.After.Name] = changed
		} else {
			changes[changed.Before.Name] = changed
		}
	}
	return changes
}
