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
	"maps"
	"slices"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/uber/tango/core/targethasher"
)

// IntSet is a set of integers.
type IntSet map[int]struct{}

// NewIntSet creates a new, empty IntSet.
func NewIntSet() IntSet { return make(IntSet) }

// Insert adds i to the set.
func (s IntSet) Insert(i int) { s[i] = struct{}{} }

// Delete removes i from the set.
func (s IntSet) Delete(i int) { delete(s, i) }

// Contains reports whether i is in the set.
func (s IntSet) Contains(i int) bool { _, ok := s[i]; return ok }

// UnsortedList returns the elements of the set as an unsorted slice.
func (s IntSet) UnsortedList() []int {
	result := make([]int, 0, len(s))
	for k := range s {
		result = append(result, k)
	}
	return result
}

// Copy returns a shallow copy of the set.
func (s IntSet) Copy() IntSet {
	c := make(IntSet, len(s))
	for k := range s {
		c[k] = struct{}{}
	}
	return c
}

// StringSet is a set of strings.
type StringSet map[string]struct{}

// NewStringSet creates a StringSet from the given values.
func NewStringSet(vals ...string) StringSet {
	s := make(StringSet, len(vals))
	for _, v := range vals {
		s[v] = struct{}{}
	}
	return s
}

// Insert adds v to the set.
func (s StringSet) Insert(v string) { s[v] = struct{}{} }

// Contains reports whether v is in the set.
func (s StringSet) Contains(v string) bool { _, ok := s[v]; return ok }

// UnsortedList returns the elements of the set as an unsorted slice.
func (s StringSet) UnsortedList() []string {
	result := make([]string, 0, len(s))
	for k := range s {
		result = append(result, k)
	}
	return result
}

// OptimizedTarget is a representation of a Target that is optimized for storage.
type OptimizedTarget struct {
	ID              int         `json:"id"`
	Hash            []byte      `json:"hash"`
	HashWithoutDeps []byte      `json:"hashWithoutDeps"`
	RuleType        int         `json:"ruleTypeID"`
	Deps            IntSet      `json:"deps"`
	ReverseDeps     IntSet      `json:"reverseDeps"`
	Tags            []int       `json:"tagIDs"`
	Root            bool        `json:"root"`
	External        bool        `json:"external"`
	Attributes      map[int]int `json:"attributes"`
}

// OptimizedGraph is a representation of a dependency graph that is optimized for storage.
type OptimizedGraph struct {
	OptimizedTargets    map[int]*OptimizedTarget `json:"optimizedTargets"`
	ExternalRuleTargets map[int]*OptimizedTarget `json:"externalRuleTargets"`

	NextTargetID        int            `json:"nextTargetID"`
	TargetNameToID      map[string]int `json:"targetNameToID"`
	TargetIDToString    map[int]string `json:"targetIDToString"`
	RuleTypeToID        map[string]int `json:"ruleTypeToID"`
	RuleTypeIDToString  map[int]string `json:"ruleTypeIDToString"`
	TagToID             map[string]int `json:"tagToID"`
	TagIDToString       map[int]string `json:"tagIDToString"`
	AttrNameToID        map[string]int `json:"attrNameToID"`
	AttrNameIDToString  map[int]string `json:"attrNameIDToString"`
	AttrValueToID       map[string]int `json:"attrValueToID"`
	AttrValueIDToString map[int]string `json:"attrValueIDToString"`
}

// Copy makes a deep copy of OptimizedTarget.
func (t *OptimizedTarget) Copy() *OptimizedTarget {
	return &OptimizedTarget{
		ID:              t.ID,
		Hash:            slices.Clone(t.Hash),
		HashWithoutDeps: slices.Clone(t.HashWithoutDeps),
		RuleType:        t.RuleType,
		Deps:            t.Deps.Copy(),
		ReverseDeps:     t.ReverseDeps.Copy(),
		Tags:            slices.Clone(t.Tags),
		Root:            t.Root,
		External:        t.External,
		Attributes:      maps.Clone(t.Attributes),
	}
}

// OptimizeGraph converts a map of Targets into an OptimizedGraph.
func OptimizeGraph(targets map[string]*targethasher.Target) *OptimizedGraph {
	g := &OptimizedGraph{
		OptimizedTargets:    make(map[int]*OptimizedTarget, len(targets)),
		ExternalRuleTargets: make(map[int]*OptimizedTarget, len(targets)),
		NextTargetID:        0,
		TargetNameToID:      make(map[string]int, len(targets)),
		TargetIDToString:    make(map[int]string, len(targets)),
		RuleTypeToID:        make(map[string]int),
		RuleTypeIDToString:  make(map[int]string),
		TagToID:             make(map[string]int),
		TagIDToString:       make(map[int]string),
		AttrNameToID:        make(map[string]int),
		AttrNameIDToString:  make(map[int]string),
		AttrValueToID:       make(map[string]int),
		AttrValueIDToString: make(map[int]string),
	}
	for _, target := range targets {
		g.AddTarget(target)
	}

	for id, target := range g.OptimizedTargets {
		for depID := range target.Deps {
			child, ok := g.OptimizedTargets[depID]
			if !ok {
				continue
			}
			child.ReverseDeps.Insert(id)
		}
	}

	return g
}

// Copy makes a deep copy of the OptimizedGraph.
func (g *OptimizedGraph) Copy() *OptimizedGraph {
	optimizedTargetsCopy := make(map[int]*OptimizedTarget, len(g.OptimizedTargets))
	for id, target := range g.OptimizedTargets {
		optimizedTargetsCopy[id] = target.Copy()
	}

	externalRuleTargetsCopy := make(map[int]*OptimizedTarget, len(g.ExternalRuleTargets))
	for id, target := range g.ExternalRuleTargets {
		externalRuleTargetsCopy[id] = target.Copy()
	}

	return &OptimizedGraph{
		OptimizedTargets:    optimizedTargetsCopy,
		ExternalRuleTargets: externalRuleTargetsCopy,
		NextTargetID:        g.NextTargetID,
		TargetNameToID:      maps.Clone(g.TargetNameToID),
		TargetIDToString:    maps.Clone(g.TargetIDToString),
		RuleTypeToID:        maps.Clone(g.RuleTypeToID),
		RuleTypeIDToString:  maps.Clone(g.RuleTypeIDToString),
		TagToID:             maps.Clone(g.TagToID),
		TagIDToString:       maps.Clone(g.TagIDToString),
		AttrNameToID:        maps.Clone(g.AttrNameToID),
		AttrNameIDToString:  maps.Clone(g.AttrNameIDToString),
		AttrValueToID:       maps.Clone(g.AttrValueToID),
		AttrValueIDToString: maps.Clone(g.AttrValueIDToString),
	}
}

// AddTarget adds a Target to the graph.
func (g *OptimizedGraph) AddTarget(target *targethasher.Target) {
	id := g.getOrGenerateTargetID(target.Name)
	depIDs := NewIntSet()
	for _, dep := range target.Deps {
		depIDs.Insert(g.getOrGenerateTargetID(dep))
	}
	optimizedTarget := &OptimizedTarget{
		ID:              id,
		Hash:            target.Hash,
		HashWithoutDeps: target.HashWithoutDeps,
		External:        target.External,
		RuleType:        getOrGenerateRecordReverse(target.RuleType, g.RuleTypeToID, g.RuleTypeIDToString),
		Deps:            depIDs,
		ReverseDeps:     NewIntSet(),
	}

	isExternalRuleTarget := target.RuleType == targethasher.ExternalRuleType
	if !isExternalRuleTarget {
		tagIDs := make([]int, len(target.Tags))
		for i, tag := range target.Tags {
			tagIDs[i] = getOrGenerateRecordReverse(tag, g.TagToID, g.TagIDToString)
		}

		attributes := make(map[int]int, len(target.Attributes))
		for _, attr := range target.Attributes {
			attrNameID := getOrGenerateRecordReverse(attr.GetName(), g.AttrNameToID, g.AttrNameIDToString)
			attrValueID := getOrGenerateRecordReverse(attr.GetStringValue(), g.AttrValueToID, g.AttrValueIDToString)
			attributes[attrNameID] = attrValueID
		}
		optimizedTarget.HashWithoutDeps = target.HashWithoutDeps
		optimizedTarget.Tags = tagIDs
		optimizedTarget.Root = target.Root
		optimizedTarget.External = target.External
		optimizedTarget.Attributes = attributes
	}

	g.OptimizedTargets[id] = optimizedTarget
	if isExternalRuleTarget {
		g.ExternalRuleTargets[id] = optimizedTarget
	}
}

// OptimizedTargetToTarget converts an OptimizedTarget back to a Target.
func (g *OptimizedGraph) OptimizedTargetToTarget(targetID int) targethasher.Target {
	name, ok := g.TargetIDToString[targetID]
	if !ok {
		return targethasher.Target{}
	}

	optimizedTarget, ok := g.OptimizedTargets[targetID]
	if !ok {
		return targethasher.Target{Name: name}
	}

	target := targethasher.Target{
		Name:            name,
		Hash:            optimizedTarget.Hash,
		HashWithoutDeps: optimizedTarget.HashWithoutDeps,
		RuleType:        g.RuleTypeIDToString[optimizedTarget.RuleType],
		Deps:            make([]string, 0, len(optimizedTarget.Deps)),
		Tags:            make([]string, len(optimizedTarget.Tags)),
		Root:            optimizedTarget.Root,
		External:        optimizedTarget.External,
		Attributes:      make([]*buildpb.Attribute, 0, len(optimizedTarget.Attributes)),
	}

	for depID := range optimizedTarget.Deps {
		target.Deps = append(target.Deps, g.TargetIDToString[depID])
	}

	for i, tagID := range optimizedTarget.Tags {
		target.Tags[i] = g.TagIDToString[tagID]
	}

	for nameID, valID := range optimizedTarget.Attributes {
		n := g.AttrNameIDToString[nameID]
		v := g.AttrValueIDToString[valID]
		target.Attributes = append(target.Attributes, &buildpb.Attribute{
			Name:        &n,
			StringValue: &v,
		})
	}

	return target
}

func (g *OptimizedGraph) getOrGenerateTargetID(targetName string) int {
	if id, ok := g.TargetNameToID[targetName]; ok {
		return id
	}

	id := g.NextTargetID
	g.NextTargetID++
	g.TargetNameToID[targetName] = id
	g.TargetIDToString[id] = targetName
	return id
}

func getOrGenerate(key string, m map[string]int) (val int) {
	val, ok := m[key]
	if !ok {
		m[key], val = len(m), len(m)
	}
	return
}

func getOrGenerateRecordReverse(key string, m map[string]int, reverseM map[int]string) int {
	id := getOrGenerate(key, m)
	reverseM[id] = key
	return id
}
