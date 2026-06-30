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

package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/uber-go/tally"
	"github.com/uber/tango/core/common"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/zap"
)

// targetGraphComparer diffs two target graph streams and produces a chunked
// GetChangedTargetsResponse stream. Created fresh per comparison call because
// the mappers accumulate per-call state.
type targetGraphComparer struct {
	scope          tally.Scope
	targetMapper   *common.NameIDMapper
	ruleTypeMapper *common.NameIDMapper
	tagMapper      *common.NameIDMapper
	attrNameMapper *common.NameIDMapper
	attrValMapper  *common.NameIDMapper

	changedTargetChunkSize int
	metadataMapChunkSize   int
}

func newTargetGraphComparer(scope tally.Scope, changedTargetChunkSize, metadataMapChunkSize int) *targetGraphComparer {
	return &targetGraphComparer{
		scope:                  scope.SubScope("compare_target_graphs"),
		targetMapper:           common.NewNameIDMapper(),
		ruleTypeMapper:         common.NewNameIDMapper(),
		tagMapper:              common.NewNameIDMapper(),
		attrNameMapper:         common.NewNameIDMapper(),
		attrValMapper:          common.NewNameIDMapper(),
		changedTargetChunkSize: changedTargetChunkSize,
		metadataMapChunkSize:   metadataMapChunkSize,
	}
}

// Compare diffs two target graph streams. Targets are classified as NEW (only
// in second), DELETED (only in first), or CHANGED (present in both, differs).
// Distances are always computed: a target is a distance-0 seed when it is
// NEW, DELETED, a source file with a changed hash, or a rule whose own
// configuration (attributes or direct deps) changed. All other CHANGED
// targets get their distance from BFS over the reverse-dep graph.
// Output IDs are re-mapped into a canonical per-call namespace so the
// response metadata only carries the names actually referenced.
func (c *targetGraphComparer) Compare(ctx context.Context, logger *zap.Logger, firstGraph, secondGraph []*pb.GetTargetGraphResponse, maxDist int32) ([]*pb.GetChangedTargetsResponse, error) {
	start := time.Now()
	logger.Info("compareTargetGraphs: Computing differences between target graphs")

	// 1) Extract targets and metadata; index by canonical names
	indexStart := time.Now()
	firstTargetsByID, firstMetadata, err := getTargetsAndMetadata(ctx, firstGraph)
	if err != nil {
		return nil, err
	}
	secondTargetsByID, secondMetadata, err := getTargetsAndMetadata(ctx, secondGraph)
	if err != nil {
		return nil, err
	}
	// Release raw chunk slices — individual target protos are now held by the ID maps.
	firstGraph = nil
	secondGraph = nil
	firstByName, err := buildNameIndex(ctx, firstTargetsByID, firstMetadata)
	if err != nil {
		return nil, err
	}
	firstTargetsByID = nil // all pointers are now in firstByName; drop the duplicate map
	secondByName, err := buildNameIndex(ctx, secondTargetsByID, secondMetadata)
	if err != nil {
		return nil, err
	}
	secondTargetsByID = nil
	indexDuration := time.Since(indexStart)
	c.scope.Timer("index_duration").Record(indexDuration)

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	sourceFileRuleTypeID := detectSourceFileID(secondMetadata)

	changedByName := make(map[string]*pb.ChangedTarget)
	seeds := make(map[string]struct{})

	getTargetId := func(name string) int32 { return c.targetMapper.ID(name) }
	getRuleTypeId := func(name string) int32 { return c.ruleTypeMapper.ID(name) }
	getTagId := func(name string) int32 { return c.tagMapper.ID(name) }
	getAttrNameId := func(name string) int32 { return c.attrNameMapper.ID(name) }
	getAttrValId := func(name string) int32 { return c.attrValMapper.ID(name) }

	// Pass 1: walk second revision. Targets not in first revision are NEW (seeds).
	// Targets in both with differing hashes are CHANGED; source-file CHANGED
	// targets are also seeds. Rules that own a changed source file are promoted
	// to seeds in pass 2 via hasChangedSourceFileDep.
	diffScanStart := time.Now()
	for name, newT := range secondByName {
		oldT, exists := firstByName[name]
		if !exists {
			changedByName[name] = &pb.ChangedTarget{
				ChangeType: pb.CHANGE_TYPE_NEW,
				NewTarget: transposeOptimizedTarget(
					newT,
					secondMetadata.GetTargetIdMapping(),
					secondMetadata.GetRuleTypeMapping(),
					secondMetadata.GetTagMapping(),
					secondMetadata.GetAttributeNameMapping(),
					secondMetadata.GetAttributeStringValueMapping(),
					getTargetId, getRuleTypeId, getTagId, getAttrNameId, getAttrValId,
				),
			}
			seeds[name] = struct{}{}
			continue
		}
		if oldT.GetHash() == newT.GetHash() {
			continue
		}
		if sourceFileRuleTypeID != -1 && newT.GetRuleType() == sourceFileRuleTypeID {
			seeds[name] = struct{}{}
		}
		newTarget := transposeOptimizedTarget(
			newT,
			secondMetadata.GetTargetIdMapping(),
			secondMetadata.GetRuleTypeMapping(),
			secondMetadata.GetTagMapping(),
			secondMetadata.GetAttributeNameMapping(),
			secondMetadata.GetAttributeStringValueMapping(),
			getTargetId, getRuleTypeId, getTagId, getAttrNameId, getAttrValId,
		)
		oldTarget := transposeOptimizedTarget(
			oldT,
			firstMetadata.GetTargetIdMapping(),
			firstMetadata.GetRuleTypeMapping(),
			firstMetadata.GetTagMapping(),
			firstMetadata.GetAttributeNameMapping(),
			firstMetadata.GetAttributeStringValueMapping(),
			getTargetId, getRuleTypeId, getTagId, getAttrNameId, getAttrValId,
		)
		changedByName[name] = &pb.ChangedTarget{
			ChangeType: pb.CHANGE_TYPE_CHANGED,
			OldTarget:  oldTarget,
			NewTarget:  newTarget,
		}
	}
	diffScanDuration := time.Since(diffScanStart)
	c.scope.Timer("diff_scan_duration").Record(diffScanDuration)

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Pass 2: decide which CHANGED rule targets are seeds (distance 0).
	classifyStart := time.Now()
	for name, ct := range changedByName {
		if _, isSeed := seeds[name]; isSeed {
			continue
		}
		if ct.GetChangeType() != pb.CHANGE_TYPE_CHANGED {
			continue
		}
		newT := secondByName[name]
		oldT := firstByName[name]

		anyChanged, depsChanged := changedDepStatus(oldT, firstMetadata, newT, secondMetadata, changedByName)
		if !anyChanged {
			seeds[name] = struct{}{}
			continue
		}
		if depsChanged {
			seeds[name] = struct{}{}
			continue
		}
		if hasChangedSourceFileDep(newT, secondMetadata, changedByName, secondByName, sourceFileRuleTypeID) {
			seeds[name] = struct{}{}
			continue
		}
		attrsChanged, err := attributesChanged(oldT, firstMetadata, newT, secondMetadata)
		if err != nil {
			return nil, fmt.Errorf("failed to check attributes changed: %w", err)
		}
		if attrsChanged {
			seeds[name] = struct{}{}
		}
	}
	classifyDuration := time.Since(classifyStart)
	c.scope.Timer("classify_duration").Record(classifyDuration)

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Pass 3: emit DELETED entries for targets present only in the first revision.
	for name, oldT := range firstByName {
		if _, exists := secondByName[name]; exists {
			continue
		}
		changedByName[name] = &pb.ChangedTarget{
			ChangeType: pb.CHANGE_TYPE_DELETED,
			OldTarget: transposeOptimizedTarget(
				oldT,
				firstMetadata.GetTargetIdMapping(),
				firstMetadata.GetRuleTypeMapping(),
				firstMetadata.GetTagMapping(),
				firstMetadata.GetAttributeNameMapping(),
				firstMetadata.GetAttributeStringValueMapping(),
				getTargetId, getRuleTypeId, getTagId, getAttrNameId, getAttrValId,
			),
		}
		seeds[name] = struct{}{}
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Distances are always computed; seeds get 0, BFS assigns 1+ to consumers.
	distancesStart := time.Now()
	if err := computeDistances(ctx, changedByName, secondByName, secondMetadata, seeds, maxDist); err != nil {
		return nil, err
	}
	distancesDuration := time.Since(distancesStart)
	c.scope.Timer("distances_duration").Record(distancesDuration)

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Collect changed targets.
	changed := make([]*pb.ChangedTarget, 0, len(changedByName))
	for _, ct := range changedByName {
		changed = append(changed, ct)
	}

	// Emit changes in chunks to stay within gRPC per-message size limits, followed by chunked metadata.
	var results []*pb.GetChangedTargetsResponse
	for i := 0; i < len(changed); i += c.changedTargetChunkSize {
		end := i + c.changedTargetChunkSize
		if end > len(changed) {
			end = len(changed)
		}
		results = append(results, &pb.GetChangedTargetsResponse{
			Item: &pb.GetChangedTargetsResponse_ChangedTargets{
				ChangedTargets: &pb.ChangedTargets{
					ChangedTargets: changed[i:end],
				},
			},
		})
	}
	if len(results) == 0 {
		results = append(results, &pb.GetChangedTargetsResponse{
			Item: &pb.GetChangedTargetsResponse_ChangedTargets{
				ChangedTargets: &pb.ChangedTargets{},
			},
		})
	}
	for _, meta := range common.ChunkMetadata(
		c.targetMapper.Invert(),
		c.ruleTypeMapper.Invert(),
		c.tagMapper.Invert(),
		c.attrNameMapper.Invert(),
		c.attrValMapper.Invert(),
		c.metadataMapChunkSize,
	) {
		results = append(results, &pb.GetChangedTargetsResponse{
			Item: &pb.GetChangedTargetsResponse_Metadata{
				Metadata: meta,
			},
		})
	}
	totalDuration := time.Since(start)
	logger.Info("compareTargetGraphs: Done",
		zap.Duration("total_duration", totalDuration),
	)
	c.scope.Timer("total_duration").Record(totalDuration)
	return results, nil
}

// cancelCheckInterval is how often long-running loops check ctx.Err().
const cancelCheckInterval = 4096

// getTargetsAndMetadata builds ID->target maps and merges metadata from a target graph stream.
// Metadata may arrive in multiple chunks (e.g. when target_id_mapping exceeds the gRPC message
// size limit); all chunks are merged into a single Metadata so callers can use it uniformly.
func getTargetsAndMetadata(ctx context.Context, graph []*pb.GetTargetGraphResponse) (map[int32]*pb.OptimizedTarget, *pb.Metadata, error) {
	targets := make(map[int32]*pb.OptimizedTarget)
	merged := &pb.Metadata{
		TargetIdMapping:             make(map[int32]string),
		RuleTypeMapping:             make(map[int32]string),
		TagMapping:                  make(map[int32]string),
		AttributeNameMapping:        make(map[int32]string),
		AttributeStringValueMapping: make(map[int32]string),
	}
	for _, chunk := range graph {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		switch item := chunk.GetItem().(type) {
		case *pb.GetTargetGraphResponse_Targets:
			for _, t := range item.Targets.GetTargets() {
				targets[t.GetId()] = t
			}
		case *pb.GetTargetGraphResponse_Metadata:
			m := item.Metadata
			for k, v := range m.GetTargetIdMapping() {
				merged.TargetIdMapping[k] = v
			}
			for k, v := range m.GetRuleTypeMapping() {
				merged.RuleTypeMapping[k] = v
			}
			for k, v := range m.GetTagMapping() {
				merged.TagMapping[k] = v
			}
			for k, v := range m.GetAttributeNameMapping() {
				merged.AttributeNameMapping[k] = v
			}
			for k, v := range m.GetAttributeStringValueMapping() {
				merged.AttributeStringValueMapping[k] = v
			}
		}
	}
	return targets, merged, nil
}

// buildNameIndex creates name->target maps using the provided metadata information.
func buildNameIndex(ctx context.Context, targetsByID map[int32]*pb.OptimizedTarget, meta *pb.Metadata) (map[string]*pb.OptimizedTarget, error) {
	byName := make(map[string]*pb.OptimizedTarget, len(targetsByID))
	i := 0
	for id, t := range targetsByID {
		if i%cancelCheckInterval == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		i++
		name, err := canonicalTargetName(id, meta)
		if err != nil {
			// If a target ID is missing in metadata, skip it.
			continue
		}
		byName[name] = t
	}
	return byName, nil
}

// detectSourceFileID returns the literal rule type name for source file if present.
func detectSourceFileID(meta *pb.Metadata) int32 {
	if meta == nil || len(meta.GetRuleTypeMapping()) == 0 {
		return -1
	}
	for id, name := range meta.GetRuleTypeMapping() {
		if name == "source file" {
			return id
		}
	}
	return -1
}

// canonicalTargetName returns a stable identifier for a target using metadata mapping when available.
func canonicalTargetName(id int32, meta *pb.Metadata) (string, error) {
	if meta != nil {
		if name, ok := meta.GetTargetIdMapping()[id]; ok && name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("target id %d not found in metadata", id)
}

// changedDepStatus reports two facts about a CHANGED rule's direct deps in a
// single pass over newTarget.GetDirectDependencies():
//   - anyChanged: at least one current direct dep is itself CHANGED between
//     the two revisions (i.e. appears as CHANGE_TYPE_CHANGED in changedByName).
//   - setDiffered: the *set of dep names* — not their hashes — differs between
//     old and new. A dep changing its hash while keeping the same name leaves
//     setDiffered false; that case is handled by BFS reaching the consumer at
//     distance >= 1.
//
// The name-set walk over oldTarget is skipped entirely when lengths already
// disagree (setDiffered is trivially true) or when anyChanged is false and
// the caller will seed the rule regardless of setDiffered.
func changedDepStatus(
	oldTarget *pb.OptimizedTarget,
	oldMeta *pb.Metadata,
	newTarget *pb.OptimizedTarget,
	newMeta *pb.Metadata,
	changedByName map[string]*pb.ChangedTarget,
) (anyChanged, setDiffered bool) {
	if newTarget == nil || newMeta == nil {
		return false, false
	}

	newDepIDs := newTarget.GetDirectDependencies()
	newIDMap := newMeta.GetTargetIdMapping()

	var oldDepIDs []int32
	var oldIDMap map[int32]string
	if oldTarget != nil && oldMeta != nil {
		oldDepIDs = oldTarget.GetDirectDependencies()
		oldIDMap = oldMeta.GetTargetIdMapping()
	}

	lengthsMatch := len(oldDepIDs) == len(newDepIDs)
	var newDepSet map[string]struct{}
	if lengthsMatch && len(newDepIDs) > 0 {
		newDepSet = make(map[string]struct{}, len(newDepIDs))
	}

	for _, depID := range newDepIDs {
		name := newIDMap[depID]
		if name == "" {
			continue
		}
		if !anyChanged {
			if ct, ok := changedByName[name]; ok && ct.GetChangeType() == pb.CHANGE_TYPE_CHANGED {
				anyChanged = true
			}
		}
		if newDepSet != nil {
			newDepSet[name] = struct{}{}
		}
	}

	if !lengthsMatch {
		return anyChanged, true
	}
	for _, depID := range oldDepIDs {
		name := oldIDMap[depID]
		if name == "" {
			continue
		}
		if _, exists := newDepSet[name]; !exists {
			return anyChanged, true
		}
	}
	return anyChanged, false
}

// hasChangedSourceFileDep reports whether any direct dependency of the given
// target is a changed source file.
func hasChangedSourceFileDep(
	target *pb.OptimizedTarget,
	meta *pb.Metadata,
	changedByName map[string]*pb.ChangedTarget,
	targetsByName map[string]*pb.OptimizedTarget,
	sourceFileRuleTypeID int32,
) bool {
	if target == nil || meta == nil || sourceFileRuleTypeID == -1 {
		return false
	}
	idMapping := meta.GetTargetIdMapping()
	for _, depID := range target.GetDirectDependencies() {
		depName := idMapping[depID]
		if depName == "" {
			continue
		}
		if _, changed := changedByName[depName]; !changed {
			continue
		}
		if depTarget, ok := targetsByName[depName]; ok && depTarget.GetRuleType() == sourceFileRuleTypeID {
			return true
		}
	}
	return false
}

// attributesChanged checks if the attributes changed between old and new targets.
func attributesChanged(oldTarget *pb.OptimizedTarget, oldMeta *pb.Metadata, newTarget *pb.OptimizedTarget, newMeta *pb.Metadata) (bool, error) {
	if oldMeta == nil || newMeta == nil {
		return false, nil
	}
	if err := validateTargetNames(oldTarget, newTarget, oldMeta, newMeta); err != nil {
		return false, err
	}

	oldAttrIDs := oldTarget.GetAttributes()
	newAttrIDs := newTarget.GetAttributes()

	if len(oldAttrIDs) != len(newAttrIDs) {
		return true, nil
	}
	if len(oldAttrIDs) == 0 {
		return false, nil
	}

	oldAttrNameMapping := oldMeta.GetAttributeNameMapping()
	oldAttrValMapping := oldMeta.GetAttributeStringValueMapping()
	newAttrNameMapping := newMeta.GetAttributeNameMapping()
	newAttrValMapping := newMeta.GetAttributeStringValueMapping()

	newAttrMap := make(map[string]string, len(newAttrIDs))
	for attrNameID, attrValID := range newAttrIDs {
		if attrName := newAttrNameMapping[attrNameID]; attrName != "" {
			newAttrMap[attrName] = newAttrValMapping[attrValID]
		}
	}

	for attrNameID, attrValID := range oldAttrIDs {
		if attrName := oldAttrNameMapping[attrNameID]; attrName != "" {
			oldVal := oldAttrValMapping[attrValID]
			newVal, exists := newAttrMap[attrName]
			if !exists || newVal != oldVal {
				return true, nil
			}
		}
	}
	return false, nil
}

// validateTargetNames checks if the target names are the same between old and new targets.
func validateTargetNames(oldTarget, newTarget *pb.OptimizedTarget, oldMeta, newMeta *pb.Metadata) error {
	oldTargetName, ok := oldMeta.GetTargetIdMapping()[oldTarget.GetId()]
	if !ok {
		return fmt.Errorf("old target id %d not found in metadata", oldTarget.GetId())
	}
	newTargetName, ok := newMeta.GetTargetIdMapping()[newTarget.GetId()]
	if !ok {
		return fmt.Errorf("new target id %d not found in metadata", newTarget.GetId())
	}
	if oldTargetName != newTargetName {
		return fmt.Errorf("target names are different %s != %s", oldTargetName, newTargetName)
	}
	return nil
}

// transposeOptimizedTarget remaps a target into the canonical ID space using name-based mappers.
func transposeOptimizedTarget(
	src *pb.OptimizedTarget,
	oldTargetIdMap map[int32]string,
	oldRuleTypeIdMap map[int32]string,
	oldTagIdMap map[int32]string,
	attrNameIdMap map[int32]string,
	attrValIdMap map[int32]string,
	getTargetId func(string) int32,
	getRuleTypeId func(string) int32,
	getTagId func(string) int32,
	getAttrNameId func(string) int32,
	getAttrValId func(string) int32,
) *pb.OptimizedTarget {
	if src == nil {
		return nil
	}
	dst := &pb.OptimizedTarget{
		Id:       getTargetId(oldTargetIdMap[src.GetId()]),
		Hash:     src.GetHash(),
		Root:     src.GetRoot(),
		External: src.GetExternal(),
	}
	// Direct deps
	deps := src.GetDirectDependencies()
	if len(deps) > 0 {
		out := make([]int32, 0, len(deps))
		for _, d := range deps {
			out = append(out, getTargetId(oldTargetIdMap[d]))
		}
		dst.DirectDependencies = out
	}
	// Rule type
	if rtName := oldRuleTypeIdMap[src.GetRuleType()]; rtName != "" {
		dst.RuleType = getRuleTypeId(rtName)
	}
	// Tags
	if tags := src.GetTags(); len(tags) > 0 {
		out := make([]int32, 0, len(tags))
		for _, tg := range tags {
			out = append(out, getTagId(oldTagIdMap[tg]))
		}
		dst.Tags = out
	}
	// Attributes
	if attrs := src.GetAttributes(); len(attrs) > 0 {
		out := make(map[int32]int32, len(attrs))
		for k, v := range attrs {
			name := attrNameIdMap[k]
			val := attrValIdMap[v]
			out[getAttrNameId(name)] = getAttrValId(val)
		}
		dst.Attributes = out
	}
	return dst
}

// computeDistances assigns each CHANGED target its BFS distance from the
// nearest distance-0 seed in the reverse-dependency graph.
func computeDistances(ctx context.Context, changedByName map[string]*pb.ChangedTarget, targetsByName map[string]*pb.OptimizedTarget, meta *pb.Metadata, seeds map[string]struct{}, maxDistance int32) error {
	if meta == nil {
		return nil
	}

	targetIDMapping := meta.GetTargetIdMapping()

	reverseDeps := make(map[string][]string, len(targetsByName))
	revDepIter := 0
	for name, t := range targetsByName {
		if revDepIter%cancelCheckInterval == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		revDepIter++
		for _, depID := range t.GetDirectDependencies() {
			depName := targetIDMapping[depID]
			if depName != "" {
				reverseDeps[depName] = append(reverseDeps[depName], name)
			}
		}
	}

	var queue []string
	visited := make(map[string]struct{}, len(changedByName))
	for name, ct := range changedByName {
		if _, isSeed := seeds[name]; isSeed {
			ct.Distance = 0
			queue = append(queue, name)
			visited[name] = struct{}{}
		} else {
			ct.Distance = -1
		}
	}

	bfsIter := 0
	for len(queue) > 0 {
		if bfsIter%cancelCheckInterval == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		bfsIter++
		current := queue[0]
		queue = queue[1:]
		currentDist := changedByName[current].GetDistance()

		for _, revDep := range reverseDeps[current] {
			if _, seen := visited[revDep]; seen {
				continue
			}
			nextDist := currentDist + 1
			if maxDistance >= 0 && nextDist > maxDistance {
				continue
			}
			visited[revDep] = struct{}{}
			queue = append(queue, revDep)

			if ct, ok := changedByName[revDep]; ok {
				ct.Distance = nextDist
			}
		}
	}
	return nil
}
