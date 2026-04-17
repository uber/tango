// Copyright (c) 2025 Uber Technologies, Inc.
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

package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/targethasher"
)

// --- getPackage ---

func TestGetPackage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"//pkg/sub:target", "pkg/sub"},
		{"//:target", ""},
		{"//external:repo", "external"},
		{"//a/b/c/d:foo", "a/b/c/d"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, getPackage(tt.input))
		})
	}
}

// --- invalidateHashRecursively ---

func TestInvalidateHashRecursively(t *testing.T) {
	t.Parallel()

	t.Run("sets hashes nil and adds to invalidated set", func(t *testing.T) {
		t.Parallel()
		// a ← b ← c; invalidate a's reverse deps → b and c become nil
		targets := map[string]*targethasher.Target{
			"//pkg:a": {Name: "//pkg:a", RuleType: "go_library", Hash: []byte{1}},
			"//pkg:b": {Name: "//pkg:b", RuleType: "go_library", Hash: []byte{2}, Deps: []string{"//pkg:a"}},
			"//pkg:c": {Name: "//pkg:c", RuleType: "go_library", Hash: []byte{3}, Deps: []string{"//pkg:b"}},
		}
		g := OptimizeGraph(targets)
		aID := g.TargetNameToID["//pkg:a"]
		bID := g.TargetNameToID["//pkg:b"]
		cID := g.TargetNameToID["//pkg:c"]

		invalidated := NewIntSet()
		require.NoError(t, g.invalidateHashRecursively(g.OptimizedTargets[aID].ReverseDeps, invalidated))

		// b and c should be invalidated (they are reverse deps of a)
		assert.Nil(t, g.OptimizedTargets[bID].Hash)
		assert.Nil(t, g.OptimizedTargets[cID].Hash)
		assert.True(t, invalidated.Contains(bID))
		assert.True(t, invalidated.Contains(cID))

		// a itself was not passed; its hash should be unchanged
		assert.NotNil(t, g.OptimizedTargets[aID].Hash)
		assert.False(t, invalidated.Contains(aID))
	})

	t.Run("targets with nil hash are skipped", func(t *testing.T) {
		t.Parallel()
		targets := map[string]*targethasher.Target{
			"//pkg:a": {Name: "//pkg:a", RuleType: "go_library"},                            // Hash=nil
			"//pkg:b": {Name: "//pkg:b", RuleType: "go_library", Deps: []string{"//pkg:a"}}, // Hash=nil
		}
		g := OptimizeGraph(targets)
		aID := g.TargetNameToID["//pkg:a"]

		invalidated := NewIntSet()
		require.NoError(t, g.invalidateHashRecursively(g.OptimizedTargets[aID].ReverseDeps, invalidated))

		// b has a nil hash and is thus skipped; invalidated stays empty
		assert.Empty(t, invalidated.UnsortedList())
	})
}

// --- removeTarget ---

func TestRemoveTarget(t *testing.T) {
	t.Parallel()

	t.Run("target removed from all maps", func(t *testing.T) {
		t.Parallel()
		targets := map[string]*targethasher.Target{
			"//pkg:a": {Name: "//pkg:a", RuleType: "go_library", Hash: []byte{1}},
			"//pkg:b": {Name: "//pkg:b", RuleType: "go_library", Hash: []byte{2}, Deps: []string{"//pkg:a"}},
		}
		g := OptimizeGraph(targets)
		bID := g.TargetNameToID["//pkg:b"]

		require.NoError(t, g.removeTarget(g.OptimizedTargets[bID], NewIntSet()))

		assert.NotContains(t, g.OptimizedTargets, bID)
		assert.NotContains(t, g.TargetNameToID, "//pkg:b")
		assert.NotContains(t, g.TargetIDToString, bID)
	})

	t.Run("dep's reverse dep reference is cleaned up", func(t *testing.T) {
		t.Parallel()
		targets := map[string]*targethasher.Target{
			"//pkg:dep": {Name: "//pkg:dep", RuleType: "go_library", Hash: []byte{1}},
			"//pkg:lib": {Name: "//pkg:lib", RuleType: "go_library", Hash: []byte{2}, Deps: []string{"//pkg:dep"}},
		}
		g := OptimizeGraph(targets)
		depID := g.TargetNameToID["//pkg:dep"]
		libID := g.TargetNameToID["//pkg:lib"]

		// Confirm lib is initially in dep's reverse deps
		assert.True(t, g.OptimizedTargets[depID].ReverseDeps.Contains(libID))

		require.NoError(t, g.removeTarget(g.OptimizedTargets[libID], NewIntSet()))

		assert.False(t, g.OptimizedTargets[depID].ReverseDeps.Contains(libID))
	})

	t.Run("dep becomes root when its last reverse dep is removed", func(t *testing.T) {
		t.Parallel()
		// go_library can be root; a is blocked by b. Removing b should make a root.
		targets := map[string]*targethasher.Target{
			"//pkg:a": {Name: "//pkg:a", RuleType: "go_library", Hash: []byte{1}, Root: false},
			"//pkg:b": {Name: "//pkg:b", RuleType: "go_library", Hash: []byte{2}, Deps: []string{"//pkg:a"}},
		}
		g := OptimizeGraph(targets)
		bID := g.TargetNameToID["//pkg:b"]
		aID := g.TargetNameToID["//pkg:a"]

		require.NoError(t, g.removeTarget(g.OptimizedTargets[bID], NewIntSet()))

		assert.True(t, g.OptimizedTargets[aID].Root)
	})

	t.Run("reverse dep hashes are invalidated", func(t *testing.T) {
		t.Parallel()
		// c depends on b; removing b should invalidate c's hash.
		targets := map[string]*targethasher.Target{
			"//pkg:a": {Name: "//pkg:a", RuleType: "go_library", Hash: []byte{1}},
			"//pkg:b": {Name: "//pkg:b", RuleType: "go_library", Hash: []byte{2}, Deps: []string{"//pkg:a"}},
			"//pkg:c": {Name: "//pkg:c", RuleType: "go_library", Hash: []byte{3}, Deps: []string{"//pkg:b"}},
		}
		g := OptimizeGraph(targets)
		bID := g.TargetNameToID["//pkg:b"]
		cID := g.TargetNameToID["//pkg:c"]

		invalidated := NewIntSet()
		require.NoError(t, g.removeTarget(g.OptimizedTargets[bID], invalidated))

		assert.Nil(t, g.OptimizedTargets[cID].Hash, "c's hash should be invalidated")
		assert.True(t, invalidated.Contains(cID))
	})
}

// --- upsertTarget ---

func TestUpsertTarget(t *testing.T) {
	t.Parallel()

	t.Run("inserts new target and wires reverse dep", func(t *testing.T) {
		t.Parallel()
		g := OptimizeGraph(map[string]*targethasher.Target{
			"//pkg:dep": {Name: "//pkg:dep", RuleType: "go_library", Hash: []byte{1}},
		})
		depID := g.TargetNameToID["//pkg:dep"]

		newTarget := &targethasher.Target{
			Name:            "//pkg:lib",
			RuleType:        "go_library",
			HashWithoutDeps: []byte{2},
			Deps:            []string{"//pkg:dep"},
		}
		require.NoError(t, g.upsertTarget(newTarget, NewIntSet()))

		libID, ok := g.TargetNameToID["//pkg:lib"]
		require.True(t, ok)
		assert.True(t, g.OptimizedTargets[libID].Deps.Contains(depID))
		assert.True(t, g.OptimizedTargets[depID].ReverseDeps.Contains(libID))
	})

	t.Run("missing dep returns error", func(t *testing.T) {
		t.Parallel()
		g := OptimizeGraph(nil)
		newTarget := &targethasher.Target{
			Name: "//pkg:lib",
			Deps: []string{"//pkg:missing"},
		}
		err := g.upsertTarget(newTarget, NewIntSet())
		assert.Error(t, err)
	})

	t.Run("updating target swaps dep references", func(t *testing.T) {
		t.Parallel()
		// lib initially depends on dep1; update it to depend on dep2 instead.
		targets := map[string]*targethasher.Target{
			"//pkg:dep1": {Name: "//pkg:dep1", RuleType: "go_library", Hash: []byte{1}},
			"//pkg:dep2": {Name: "//pkg:dep2", RuleType: "go_library", Hash: []byte{2}},
			"//pkg:lib":  {Name: "//pkg:lib", RuleType: "go_library", Hash: []byte{3}, Deps: []string{"//pkg:dep1"}},
		}
		g := OptimizeGraph(targets)
		dep1ID := g.TargetNameToID["//pkg:dep1"]
		dep2ID := g.TargetNameToID["//pkg:dep2"]
		libID := g.TargetNameToID["//pkg:lib"]

		updated := &targethasher.Target{
			Name: "//pkg:lib",
			Deps: []string{"//pkg:dep2"},
		}
		require.NoError(t, g.upsertTarget(updated, NewIntSet()))

		assert.True(t, g.OptimizedTargets[libID].Deps.Contains(dep2ID), "lib should now dep on dep2")
		assert.False(t, g.OptimizedTargets[libID].Deps.Contains(dep1ID), "lib should no longer dep on dep1")
		assert.False(t, g.OptimizedTargets[dep1ID].ReverseDeps.Contains(libID), "dep1 should lose lib as reverse dep")
		assert.True(t, g.OptimizedTargets[dep2ID].ReverseDeps.Contains(libID), "dep2 should gain lib as reverse dep")
	})

	t.Run("target with nil hash added to invalidated set", func(t *testing.T) {
		t.Parallel()
		g := OptimizeGraph(map[string]*targethasher.Target{
			"//pkg:dep": {Name: "//pkg:dep", RuleType: "go_library", Hash: []byte{1}},
		})
		newTarget := &targethasher.Target{
			Name: "//pkg:lib",
			Hash: nil, // no hash yet
			Deps: []string{"//pkg:dep"},
		}
		invalidated := NewIntSet()
		require.NoError(t, g.upsertTarget(newTarget, invalidated))

		libID := g.TargetNameToID["//pkg:lib"]
		assert.True(t, invalidated.Contains(libID), "target with nil hash should be in invalidated set")
	})
}
