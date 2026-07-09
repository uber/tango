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

// Package targetdiff compares target graphs across revisions.
package targetdiff

import "context"

const _cancelCheckInteral = 4096

const _sourceFileRuleType = "source file"

// Target describes a build target using semantic names.
type Target struct {
	// Name is the target's canonical build label.
	Name string
	// Hash identifies the target's state, including relevant dependencies.
	Hash string
	// RuleType identifies the kind of build rule or source file.
	RuleType string
	// Dependencies contains the names of the target's direct dependencies.
	Dependencies []string
	// Tags contains the target's build tags.
	Tags []string
	// Attributes maps build attribute names to their values.
	Attributes map[string]string
	// Root reports whether the target is a root of the target graph.
	Root bool
	// External reports whether the target belongs to an external repository.
	External bool
}

// Graph contains targets keyed by target name.
type Graph map[string]*Target

// ChangeType classifies how a target changed between revisions.
type ChangeType int

const (
	ChangeTypeInvalid ChangeType = iota
	ChangeTypeNew
	ChangeTypeDeleted
	ChangeTypeChanged
)

// ChangedTarget describes one target that differs between revisions.
type ChangedTarget struct {
	// ChangeType classifies the difference between revisions.
	ChangeType ChangeType
	// Before is the target in the earlier revision, or nil for a new target.
	Before *Target
	// After is the target in the later revision, or nil for a deleted target.
	After *Target
	// Distance is the reverse-dependency distance from the nearest direct change,
	// or -1 when no direct change is reachable within the requested limit.
	Distance int32
}

// Request describes two target graphs to compare.
type Request struct {
	// Before is the target graph from the earlier revision.
	Before Graph
	// After is the target graph from the later revision.
	After Graph

	// MaxDistance limits reverse-dependency traversal when non-negative.
	MaxDistance int32
}

// Result contains targets that differ between revisions.
type Result struct {
	// ChangedTargets contains the classified differences between the graphs.
	ChangedTargets []*ChangedTarget
}

// Compare classifies target changes and computes each change's distance from
// the nearest target whose own state changed.
func Compare(ctx context.Context, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	changedByName := make(map[string]*ChangedTarget)
	seeds := make(map[string]struct{})
	i := 0
	for name, afterTarget := range request.After {
		if i%_cancelCheckInteral == 0 {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
		}
		i++

		beforeTarget, exists := request.Before[name]
		if !exists {
			changedByName[name] = &ChangedTarget{
				ChangeType: ChangeTypeNew,
				After:      afterTarget,
			}
			seeds[name] = struct{}{}
			continue
		}
		if beforeTarget.Hash == afterTarget.Hash {
			continue
		}
		if afterTarget.RuleType == _sourceFileRuleType {
			seeds[name] = struct{}{}
		}
		changedByName[name] = &ChangedTarget{
			ChangeType: ChangeTypeChanged,
			Before:     beforeTarget,
			After:      afterTarget,
		}
	}

	for name, changed := range changedByName {
		if _, isSeed := seeds[name]; isSeed || changed.ChangeType != ChangeTypeChanged {
			continue
		}
		afterTarget := request.After[name]
		beforeTarget := request.Before[name]
		anyChanged, dependenciesChanged := changedDependencyStatus(beforeTarget, afterTarget, changedByName)
		if !anyChanged || dependenciesChanged {
			seeds[name] = struct{}{}
			continue
		}
		if hasChangedSourceFileDependency(afterTarget, changedByName, request.After) {
			seeds[name] = struct{}{}
			continue
		}
		if attributesChanged(beforeTarget.Attributes, afterTarget.Attributes) {
			seeds[name] = struct{}{}
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	i = 0
	for name, beforeTarget := range request.Before {
		if i%_cancelCheckInteral == 0 {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
		}
		i++
		if _, exists := request.After[name]; exists {
			continue
		}
		changedByName[name] = &ChangedTarget{
			ChangeType: ChangeTypeDeleted,
			Before:     beforeTarget,
		}
		seeds[name] = struct{}{}
	}
	if err := computeDistances(ctx, changedByName, request.After, seeds, request.MaxDistance); err != nil {
		return Result{}, err
	}

	changedTargets := make([]*ChangedTarget, 0, len(changedByName))
	for _, changed := range changedByName {
		changedTargets = append(changedTargets, changed)
	}
	return Result{ChangedTargets: changedTargets}, nil
}

func changedDependencyStatus(
	beforeTarget *Target,
	afterTarget *Target,
	changedByName map[string]*ChangedTarget,
) (anyChanged, setChanged bool) {
	if afterTarget == nil {
		return false, false
	}

	var beforeDependencies []string
	if beforeTarget != nil {
		beforeDependencies = beforeTarget.Dependencies
	}
	lengthsMatch := len(beforeDependencies) == len(afterTarget.Dependencies)
	var afterDependencies map[string]struct{}
	if lengthsMatch && len(afterTarget.Dependencies) > 0 {
		afterDependencies = make(map[string]struct{}, len(afterTarget.Dependencies))
	}
	for _, dependency := range afterTarget.Dependencies {
		if changed, ok := changedByName[dependency]; ok && changed.ChangeType == ChangeTypeChanged {
			anyChanged = true
		}
		if afterDependencies != nil {
			afterDependencies[dependency] = struct{}{}
		}
	}
	if !lengthsMatch {
		return anyChanged, true
	}
	for _, dependency := range beforeDependencies {
		if _, exists := afterDependencies[dependency]; !exists {
			return anyChanged, true
		}
	}
	return anyChanged, false
}

func hasChangedSourceFileDependency(
	target *Target,
	changedByName map[string]*ChangedTarget,
	targetsByName Graph,
) bool {
	if target == nil {
		return false
	}
	for _, dependencyName := range target.Dependencies {
		if _, changed := changedByName[dependencyName]; !changed {
			continue
		}
		dependency := targetsByName[dependencyName]
		if dependency != nil && dependency.RuleType == _sourceFileRuleType {
			return true
		}
	}
	return false
}

func attributesChanged(before, after map[string]string) bool {
	if len(before) != len(after) {
		return true
	}
	for name, value := range before {
		if afterValue, exists := after[name]; !exists || afterValue != value {
			return true
		}
	}
	return false
}

func computeDistances(
	ctx context.Context,
	changedByName map[string]*ChangedTarget,
	targetsByName Graph,
	seeds map[string]struct{},
	maxDistance int32,
) error {
	reverseDependencies := make(map[string][]string, len(targetsByName))
	i := 0
	for name, target := range targetsByName {
		if i%_cancelCheckInteral == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		i++
		for _, dependencyName := range target.Dependencies {
			if dependencyName != "" {
				reverseDependencies[dependencyName] = append(reverseDependencies[dependencyName], name)
			}
		}
	}

	var queue []string
	visited := make(map[string]struct{}, len(changedByName))
	for name, changed := range changedByName {
		if _, seed := seeds[name]; seed {
			changed.Distance = 0
			queue = append(queue, name)
			visited[name] = struct{}{}
		} else {
			changed.Distance = -1
		}
	}
	for i := 0; len(queue) > 0; i++ {
		if i%_cancelCheckInteral == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		current := queue[0]
		queue = queue[1:]
		for _, dependent := range reverseDependencies[current] {
			if _, seen := visited[dependent]; seen {
				continue
			}
			changed := changedByName[dependent]
			if changed == nil {
				continue
			}
			distance := changedByName[current].Distance + 1
			if maxDistance >= 0 && distance > maxDistance {
				continue
			}
			visited[dependent] = struct{}{}
			queue = append(queue, dependent)
			changed.Distance = distance
		}
	}
	return nil
}
