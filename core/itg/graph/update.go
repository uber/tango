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
	"crypto/sha1"
	"fmt"
	"slices"
	"strings"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	set "github.com/deckarep/golang-set/v2"
	"github.com/uber/tango/core/targethasher"
)

// TODO: properly read it from changed_target_config.yaml
var sequentialHashTargets = []string{"@io_bazel_rules_go//:go_context_data"}

// UpdateGraphInput contains the parameters to update the graph.
type UpdateGraphInput struct {
	DeletedSrcFiles StringSet
	ChangedPkgs     StringSet
	DeletedPkgs     StringSet
	QueryResult     *buildpb.QueryResult
	WorkspaceRoot   string
	FullHashRepos   StringSet
	UseBzlmod       bool
}

// UpdateGraph updates the dependency relationships and hashes of targets in the graph.
func (g *OptimizedGraph) UpdateGraph(
	ctx context.Context,
	sourceHasher targethasher.SourceHasher,
	input UpdateGraphInput,
) error {
	rawQueryResults := input.QueryResult

	// translate raw query results to targethasher.Target
	warns := make(map[string]error)
	targets, err := targethasher.GetInternalTargetsWithoutHashAndRootInfo(ctx, rawQueryResults)
	if err != nil {
		return err
	}

	fullHashReposSet := set.NewSet(input.FullHashRepos.UnsortedList()...)

	// HashExternalTargets adds external rule targets and hashes them
	if err := targethasher.HashExternalTargets(ctx, rawQueryResults, targets, sourceHasher, input.WorkspaceRoot, fullHashReposSet, warns, input.UseBzlmod); err != nil {
		return err
	}

	allInvalidated := NewIntSet()
	// update external rule targets in the graph
	for _, target := range targets {
		if target.RuleType == targethasher.ExternalRuleType {
			if err := g.upsertExternalRuleTarget(target, allInvalidated); err != nil {
				return err
			}
		}
	}
	// retrieve all external targets and convert to targethasher.Target, needed by the hasher to hash source files
	for id, target := range g.ExternalRuleTargets {
		name := g.TargetIDToString[id]
		if _, exists := targets[name]; !exists {
			targets[name] = &targethasher.Target{
				Name:            name,
				RuleType:        targethasher.ExternalRuleType,
				Hash:            target.Hash,
				HashWithoutDeps: target.HashWithoutDeps,
				External:        target.External,
			}
		}
	}
	// compute hashes for source file, package group, and rule common targets
	if err := computeAvailableHashes(sourceHasher, targets); err != nil {
		return err
	}

	// update and invalidate targets
	if err := g.updateInvalidateTargets(ctx, input.DeletedSrcFiles, input.ChangedPkgs, input.DeletedPkgs, targets, allInvalidated); err != nil {
		return err
	}

	// prioritize computing hashes of targets that could create cycles
	for _, target := range sequentialHashTargets {
		id, ok := g.TargetNameToID[target]
		if !ok || !allInvalidated.Contains(id) {
			continue
		}
		if _, err := g.computeHashes(ctx, id); err != nil {
			return err
		}
	}

	// update hashes for invalidated targets
	for id := range allInvalidated {
		if _, err := g.computeHashes(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// upsertExternalRuleTarget inserts or updates an external rule target and invalidates reverse deps.
func (g *OptimizedGraph) upsertExternalRuleTarget(target *targethasher.Target, invalidated IntSet) error {
	id := g.getOrGenerateTargetID(target.Name)
	ruleTypeID := getOrGenerateRecordReverse(target.RuleType, g.RuleTypeToID, g.RuleTypeIDToString)
	depIDs := NewIntSet()
	for _, dep := range target.Deps {
		depIDs.Insert(g.getOrGenerateTargetID(dep))
	}

	if oldTarget, ok := g.OptimizedTargets[id]; ok {
		oldTarget.Hash = target.Hash
		oldTarget.Deps = depIDs
		oldTarget.HashWithoutDeps = target.HashWithoutDeps
		oldTarget.External = target.External
		return g.invalidateHashRecursively(oldTarget.ReverseDeps, invalidated)
	}

	optimizedTarget := &OptimizedTarget{
		ID:              id,
		Hash:            target.Hash,
		HashWithoutDeps: target.HashWithoutDeps,
		RuleType:        ruleTypeID,
		Deps:            depIDs,
		ReverseDeps:     NewIntSet(),
		External:        target.External,
	}
	g.OptimizedTargets[id] = optimizedTarget
	g.ExternalRuleTargets[id] = optimizedTarget
	return nil
}

// computeAvailableHashes computes hashes that are available without dep traversal.
func computeAvailableHashes(
	hasher targethasher.SourceHasher,
	targets map[string]*targethasher.Target,
) error {
	for name, target := range targets {
		var hash []byte
		var hashWithoutDeps []byte
		switch target.RuleType {
		case targethasher.GeneratedFileType:
			// skip: generated files derive their hash from their generating rule
		case targethasher.PackageGroup:
			h := sha1.New()
			h.Write([]byte(name))
			hash = h.Sum(nil)
		case targethasher.SourceFileType:
			h, err := hasher.HashSourceFile(target.SourceFile)
			if err != nil {
				return err
			}
			hash = h
		case targethasher.ExternalRuleType:
			continue
		default:
			noDepsHasher := sha1.New()
			targethasher.HashRuleCommon(target.Rule, noDepsHasher)
			hashWithoutDeps = noDepsHasher.Sum(nil)
		}
		if hash != nil {
			target.Hash = hash
		}
		if hashWithoutDeps != nil {
			target.HashWithoutDeps = hashWithoutDeps
		}
	}
	return nil
}

// computeHashes computes hashes recursively for the given target ID.
func (g *OptimizedGraph) computeHashes(ctx context.Context, id int) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if externalRuleTarget, ok := g.ExternalRuleTargets[id]; ok {
		return externalRuleTarget.Hash, nil
	}

	target, ok := g.OptimizedTargets[id]
	if !ok {
		return nil, fmt.Errorf("target %d not found in graph", id)
	}
	if target.Hash != nil {
		return target.Hash, nil
	}

	// mark as visiting to handle cycles
	target.Hash = []byte{}
	var hash []byte
	switch g.RuleTypeIDToString[target.RuleType] {
	case targethasher.SourceFileType, targethasher.PackageGroup:
		return nil, fmt.Errorf("source file or package group target %s should already have hash", g.TargetIDToString[id])
	case targethasher.GeneratedFileType:
		var singleDep int
		for dep := range target.Deps {
			singleDep = dep
			break
		}
		dephash, err := g.computeHashes(ctx, singleDep)
		if err != nil {
			return nil, err
		}
		hash = dephash
	default:
		if target.HashWithoutDeps == nil {
			return nil, fmt.Errorf("rule target %s should already have rule hash", g.TargetIDToString[id])
		}
		h := sha1.New()
		h.Write(target.HashWithoutDeps)
		depIDs := target.Deps.UnsortedList()
		slices.SortStableFunc(depIDs, func(i, j int) int {
			return strings.Compare(g.TargetIDToString[i], g.TargetIDToString[j])
		})
		for _, dep := range depIDs {
			dephash, err := g.computeHashes(ctx, dep)
			if err != nil {
				return nil, err
			}
			h.Write(dephash)
		}
		hash = h.Sum(nil)
	}
	if hash != nil {
		target.Hash = hash
	}
	return hash, nil
}
