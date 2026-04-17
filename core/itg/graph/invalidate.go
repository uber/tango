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
	"context"
	"fmt"
	"strings"

	"github.com/uber/tango/core/targethasher"
)

// getTargetByID looks up a target by ID in both OptimizedTargets and ExternalRuleTargets.
func (g *OptimizedGraph) getTargetByID(id int) *OptimizedTarget {
	if target := g.OptimizedTargets[id]; target != nil {
		return target
	}
	return g.ExternalRuleTargets[id]
}

// updateInvalidateTargets updates and invalidates targets.
func (g *OptimizedGraph) updateInvalidateTargets(
	ctx context.Context,
	deletedSrcFileTargets StringSet,
	changedPkgs StringSet,
	deletedPkgs StringSet,
	targets map[string]*targethasher.Target,
	allInvalidated IntSet,
) error {
	for deletedSrcFileTarget := range deletedSrcFileTargets {
		if toDeleteID, ok := g.TargetNameToID[deletedSrcFileTarget]; ok {
			if err := g.removeTarget(g.OptimizedTargets[toDeleteID], allInvalidated); err != nil {
				return err
			}
		}
	}

	if err := g.checkDeletedTargets(changedPkgs, deletedPkgs, targets, allInvalidated); err != nil {
		return err
	}

	topoSortedTargets, err := topoSort(ctx, targets)
	if err != nil {
		return err
	}
	for _, t := range topoSortedTargets {
		if err := g.upsertTarget(t, allInvalidated); err != nil {
			return err
		}
	}
	return nil
}

// removeTarget removes the target from the graph.
func (g *OptimizedGraph) removeTarget(target *OptimizedTarget, invalidated IntSet) error {
	id := target.ID
	for reverseDepID := range target.ReverseDeps {
		reverseDepTarget, ok := g.OptimizedTargets[reverseDepID]
		if !ok {
			return fmt.Errorf("target %d not found", reverseDepID)
		}
		reverseDepTarget.Deps.Delete(id)
	}
	for depID := range target.Deps {
		depTarget, ok := g.OptimizedTargets[depID]
		if !ok {
			return fmt.Errorf("target %d not found", depID)
		}
		depTarget.ReverseDeps.Delete(id)
		if len(depTarget.ReverseDeps) == 0 && targethasher.CanBeRoot(g.RuleTypeIDToString[depTarget.RuleType]) {
			depTarget.Root = true
		}
	}
	delete(g.OptimizedTargets, id)
	delete(g.ExternalRuleTargets, id)
	delete(g.TargetNameToID, g.TargetIDToString[id])
	delete(g.TargetIDToString, id)
	invalidated.Delete(id)
	return g.invalidateHashRecursively(target.ReverseDeps, invalidated)
}

// invalidateHashRecursively invalidates hashes of the given targets and all their reverse deps.
func (g *OptimizedGraph) invalidateHashRecursively(ids IntSet, invalidated IntSet) error {
	candidates := make([]int, 0, len(ids))
	for id := range ids {
		candidates = append(candidates, id)
	}
	curIdx := 0
	for curIdx < len(candidates) {
		targetID := candidates[curIdx]
		curIdx++
		target, ok := g.OptimizedTargets[targetID]
		if !ok || target.Hash == nil {
			continue
		}

		target.Hash = nil
		invalidated.Insert(targetID)
		for id := range target.ReverseDeps {
			candidates = append(candidates, id)
		}
	}
	return nil
}

// checkDeletedTargets removes targets that have been deleted from the graph.
func (g *OptimizedGraph) checkDeletedTargets(changedPkgs StringSet, deletedPkgs StringSet, targets map[string]*targethasher.Target, invalidated IntSet) error {
	targetIDsInQuery := NewIntSet()
	for name := range targets {
		if id, ok := g.TargetNameToID[name]; ok {
			targetIDsInQuery.Insert(id)
		}
	}
	for _, target := range g.OptimizedTargets {
		pkgName := getPackage(g.TargetIDToString[target.ID])
		if deletedPkgs.Contains(pkgName) || (changedPkgs.Contains(pkgName) && !targetIDsInQuery.Contains(target.ID)) {
			if err := g.removeTarget(target, invalidated); err != nil {
				return err
			}
		}
	}
	return nil
}

// upsertTarget inserts or updates a target in the graph.
func (g *OptimizedGraph) upsertTarget(target *targethasher.Target, invalidated IntSet) error {
	id := g.getOrGenerateTargetID(target.Name)
	ruleTypeID := getOrGenerateRecordReverse(target.RuleType, g.RuleTypeToID, g.RuleTypeIDToString)
	depIDs := NewIntSet()
	for _, dep := range target.Deps {
		depID, ok := g.TargetNameToID[dep]
		if !ok {
			return fmt.Errorf("dependency %s of target %s not found", dep, target.Name)
		}
		depIDs.Insert(depID)

		depTarget := g.getTargetByID(depID)
		if depTarget == nil {
			return fmt.Errorf("dependency target %s (id=%d) not found in graph", dep, depID)
		}
		if depTarget.ReverseDeps == nil {
			depTarget.ReverseDeps = NewIntSet()
		}
		depTarget.ReverseDeps.Insert(id)
		depTarget.Root = false
	}

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

	if oldTarget, ok := g.OptimizedTargets[id]; ok {
		for oldDep := range oldTarget.Deps {
			if !depIDs.Contains(oldDep) {
				oldDepTarget := g.getTargetByID(oldDep)
				if oldDepTarget == nil {
					continue
				}
				oldDepTarget.ReverseDeps.Delete(id)
				if len(oldDepTarget.ReverseDeps) == 0 && targethasher.CanBeRoot(g.RuleTypeIDToString[oldDepTarget.RuleType]) {
					oldDepTarget.Root = true
				}
			}
		}
		oldTarget.Hash = target.Hash
		oldTarget.HashWithoutDeps = target.HashWithoutDeps
		oldTarget.RuleType = ruleTypeID
		oldTarget.Deps = depIDs
		oldTarget.Tags = tagIDs
		oldTarget.External = target.External
		oldTarget.Attributes = attributes
	} else {
		g.OptimizedTargets[id] = &OptimizedTarget{
			ID:              id,
			Hash:            target.Hash,
			HashWithoutDeps: target.HashWithoutDeps,
			RuleType:        ruleTypeID,
			Deps:            depIDs,
			ReverseDeps:     NewIntSet(),
			Tags:            tagIDs,
			Root:            targethasher.CanBeRoot(target.RuleType),
			External:        target.External,
			Attributes:      attributes,
		}
	}

	if err := g.invalidateHashRecursively(g.OptimizedTargets[id].ReverseDeps, invalidated); err != nil {
		return err
	}
	if target.Hash == nil {
		invalidated.Insert(id)
	}
	return nil
}

func getPackage(targetName string) string {
	before, _, _ := strings.Cut(targetName, ":")
	return strings.TrimPrefix(before, "//")
}

func topoSort(ctx context.Context, targets map[string]*targethasher.Target) ([]*targethasher.Target, error) {
	var err error
	roots := targethasher.GetTopologicalRootsAndIdentifyBuildableRoots(targets)

	targetNames := make([]string, 0, len(targets))
	visited := make(map[string]struct{}, len(targets))
	for _, name := range roots {
		targetNames, err = targethasher.ToposortRecursively(ctx, targets, name, targetNames, visited)
		if err != nil {
			return nil, err
		}
	}
	topoSortedTargets := make([]*targethasher.Target, 0, len(targetNames))
	for _, name := range targetNames {
		topoSortedTargets = append(topoSortedTargets, targets[name])
	}
	return topoSortedTargets, nil
}
