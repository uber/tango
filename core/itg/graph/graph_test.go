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
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/targethasher"
)

// --- IntSet ---

func TestIntSet(t *testing.T) {
	t.Parallel()

	t.Run("insert and contains", func(t *testing.T) {
		t.Parallel()
		s := NewIntSet()
		s.Insert(1)
		s.Insert(2)
		assert.True(t, s.Contains(1))
		assert.True(t, s.Contains(2))
		assert.False(t, s.Contains(3))
	})

	t.Run("delete removes element", func(t *testing.T) {
		t.Parallel()
		s := NewIntSet()
		s.Insert(5)
		s.Delete(5)
		assert.False(t, s.Contains(5))
	})

	t.Run("delete on absent element is safe", func(t *testing.T) {
		t.Parallel()
		s := NewIntSet()
		s.Delete(99) // should not panic
		assert.False(t, s.Contains(99))
	})

	t.Run("UnsortedList has all elements", func(t *testing.T) {
		t.Parallel()
		s := NewIntSet()
		s.Insert(3)
		s.Insert(1)
		s.Insert(2)
		list := s.UnsortedList()
		sort.Ints(list)
		assert.Equal(t, []int{1, 2, 3}, list)
	})

	t.Run("Copy is independent of original", func(t *testing.T) {
		t.Parallel()
		s := NewIntSet()
		s.Insert(10)
		c := s.Copy()

		// Mutate original and copy independently
		s.Insert(20)
		c.Insert(30)

		assert.True(t, s.Contains(20))
		assert.False(t, s.Contains(30))
		assert.False(t, c.Contains(20))
		assert.True(t, c.Contains(30))
	})
}

// --- StringSet ---

func TestStringSet(t *testing.T) {
	t.Parallel()

	t.Run("NewStringSet initializes with values", func(t *testing.T) {
		t.Parallel()
		s := NewStringSet("a", "b", "c")
		assert.True(t, s.Contains("a"))
		assert.True(t, s.Contains("b"))
		assert.True(t, s.Contains("c"))
		assert.False(t, s.Contains("d"))
	})

	t.Run("NewStringSet empty", func(t *testing.T) {
		t.Parallel()
		s := NewStringSet()
		assert.False(t, s.Contains("x"))
		assert.Empty(t, s.UnsortedList())
	})

	t.Run("Insert adds element", func(t *testing.T) {
		t.Parallel()
		s := NewStringSet()
		s.Insert("hello")
		assert.True(t, s.Contains("hello"))
	})

	t.Run("UnsortedList has all elements", func(t *testing.T) {
		t.Parallel()
		s := NewStringSet("x", "y", "z")
		list := s.UnsortedList()
		sort.Strings(list)
		assert.Equal(t, []string{"x", "y", "z"}, list)
	})
}

// --- OptimizeGraph ---

func TestOptimizeGraph(t *testing.T) {
	t.Parallel()

	t.Run("nil targets produces empty graph", func(t *testing.T) {
		t.Parallel()
		g := OptimizeGraph(nil)
		assert.Empty(t, g.OptimizedTargets)
		assert.Empty(t, g.ExternalRuleTargets)
	})

	t.Run("single target is registered with ID mappings", func(t *testing.T) {
		t.Parallel()
		targets := map[string]*targethasher.Target{
			"//pkg:a": {Name: "//pkg:a", RuleType: "go_library", Hash: []byte{1}},
		}
		g := OptimizeGraph(targets)

		id, ok := g.TargetNameToID["//pkg:a"]
		require.True(t, ok)
		assert.Equal(t, "//pkg:a", g.TargetIDToString[id])
		assert.Equal(t, []byte{1}, g.OptimizedTargets[id].Hash)
	})

	t.Run("dependency wires reverse dep", func(t *testing.T) {
		t.Parallel()
		targets := map[string]*targethasher.Target{
			"//pkg:a": {Name: "//pkg:a", RuleType: "go_library"},
			"//pkg:b": {Name: "//pkg:b", RuleType: "go_library", Deps: []string{"//pkg:a"}},
		}
		g := OptimizeGraph(targets)

		aID := g.TargetNameToID["//pkg:a"]
		bID := g.TargetNameToID["//pkg:b"]

		assert.True(t, g.OptimizedTargets[bID].Deps.Contains(aID), "b should dep on a")
		assert.True(t, g.OptimizedTargets[aID].ReverseDeps.Contains(bID), "a should have b as reverse dep")
	})

	t.Run("external rule target tracked in ExternalRuleTargets", func(t *testing.T) {
		t.Parallel()
		targets := map[string]*targethasher.Target{
			"//external:repo": {Name: "//external:repo", RuleType: targethasher.ExternalRuleType},
		}
		g := OptimizeGraph(targets)

		id := g.TargetNameToID["//external:repo"]
		assert.Contains(t, g.ExternalRuleTargets, id)
		assert.Contains(t, g.OptimizedTargets, id)
	})

	t.Run("rule type and tag ID mappings are populated", func(t *testing.T) {
		t.Parallel()
		targets := map[string]*targethasher.Target{
			"//pkg:a": {Name: "//pkg:a", RuleType: "go_library", Tags: []string{"manual"}},
		}
		g := OptimizeGraph(targets)

		_, ok := g.RuleTypeToID["go_library"]
		assert.True(t, ok, "rule type should be in RuleTypeToID")
		_, ok = g.TagToID["manual"]
		assert.True(t, ok, "tag should be in TagToID")
	})
}

// --- OptimizedTarget.Copy ---

func TestOptimizedTargetCopy(t *testing.T) {
	t.Parallel()

	original := &OptimizedTarget{
		ID:              1,
		Hash:            []byte{0xAA, 0xBB},
		HashWithoutDeps: []byte{0xCC},
		RuleType:        2,
		Deps:            IntSet{3: {}, 4: {}},
		ReverseDeps:     IntSet{5: {}},
		Tags:            []int{6, 7},
		Root:            true,
		External:        false,
		Attributes:      map[int]int{8: 9},
	}

	c := original.Copy()

	// Values are the same
	assert.Equal(t, original.ID, c.ID)
	assert.Equal(t, original.Hash, c.Hash)
	assert.Equal(t, original.HashWithoutDeps, c.HashWithoutDeps)
	assert.Equal(t, original.Root, c.Root)

	// Mutating copy's sets/slices does not affect original
	c.Deps.Insert(99)
	c.ReverseDeps.Insert(99)
	c.Tags = append(c.Tags, 99)
	c.Attributes[99] = 99
	c.Hash[0] = 0xFF

	assert.False(t, original.Deps.Contains(99))
	assert.False(t, original.ReverseDeps.Contains(99))
	assert.Len(t, original.Tags, 2)
	assert.NotContains(t, original.Attributes, 99)
	assert.Equal(t, byte(0xAA), original.Hash[0])
}

// --- OptimizedGraph.Copy ---

func TestOptimizedGraphCopy(t *testing.T) {
	t.Parallel()

	targets := map[string]*targethasher.Target{
		"//pkg:a": {Name: "//pkg:a", RuleType: "go_library", Hash: []byte{1}},
		"//pkg:b": {Name: "//pkg:b", RuleType: "go_library", Deps: []string{"//pkg:a"}},
	}
	g := OptimizeGraph(targets)
	c := g.Copy()

	// Adding a target to the copy's map does not affect the original
	aID := g.TargetNameToID["//pkg:a"]
	delete(c.OptimizedTargets, aID)
	assert.Contains(t, g.OptimizedTargets, aID, "deleting from copy should not affect original")

	// Mutating a target in the copy does not affect the original
	bID := g.TargetNameToID["//pkg:b"]
	c.OptimizedTargets[bID].Hash = []byte{0xFF}
	assert.NotEqual(t, []byte{0xFF}, g.OptimizedTargets[bID].Hash)
}

// --- OptimizedTargetToTarget ---

func TestOptimizedTargetToTarget(t *testing.T) {
	t.Parallel()

	t.Run("unknown target ID returns zero value", func(t *testing.T) {
		t.Parallel()
		g := OptimizeGraph(nil)
		result := g.OptimizedTargetToTarget(999)
		assert.Equal(t, targethasher.Target{}, result)
	})

	t.Run("known target fields round-trip", func(t *testing.T) {
		t.Parallel()
		hash := []byte{0xDE, 0xAD, 0xBE, 0xEF}
		targets := map[string]*targethasher.Target{
			"//pkg:a": {Name: "//pkg:a", RuleType: "go_library", Hash: hash, Root: true, Tags: []string{"manual"}},
		}
		g := OptimizeGraph(targets)
		aID := g.TargetNameToID["//pkg:a"]

		result := g.OptimizedTargetToTarget(aID)
		assert.Equal(t, "//pkg:a", result.Name)
		assert.Equal(t, "go_library", result.RuleType)
		assert.Equal(t, hash, result.Hash)
		assert.True(t, result.Root)
		assert.Contains(t, result.Tags, "manual")
	})

	t.Run("dep names are resolved in result", func(t *testing.T) {
		t.Parallel()
		targets := map[string]*targethasher.Target{
			"//pkg:dep": {Name: "//pkg:dep", RuleType: "go_library"},
			"//pkg:lib": {Name: "//pkg:lib", RuleType: "go_library", Deps: []string{"//pkg:dep"}},
		}
		g := OptimizeGraph(targets)
		libID := g.TargetNameToID["//pkg:lib"]

		result := g.OptimizedTargetToTarget(libID)
		assert.Contains(t, result.Deps, "//pkg:dep")
	})
}
