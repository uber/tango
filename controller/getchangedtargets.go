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
	"errors"
	"fmt"
	"time"

	"github.com/uber-go/tally"
	"github.com/uber/tango/core/common"
	"github.com/uber/tango/core/storage"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/zap"
)

// GetChangedTargets returns the changed targets between two revisions. If the
// client disconnects, the stream's context is cancelled and the function
// returns with context.Canceled.
func (c *controller) GetChangedTargets(request *pb.GetChangedTargetsRequest, stream pb.TangoServiceGetChangedTargetsYARPCServer) (retErr error) {
	scope := c.scope.SubScope("get_changed_targets")
	scope.Counter("calls").Inc(1)
	defer recordRPCResult(scope, &retErr)

	if err := validateGetChangedTargetsRequest(request); err != nil {
		c.logger.Error("GetChangedTargets: Invalid request", zap.Error(err))
		return common.WithReason(failureReasonValidation, common.ErrorTypeUser, err)
	}

	first, second := request.GetFirstRevision(), request.GetSecondRevision()
	scope = scope.Tagged(map[string]string{"repo": common.ToShortRemote(first.GetRemote())})
	ctx := stream.Context()
	start := time.Now()
	logger := c.logger.With(
		zap.Any("first_revision", first),
		zap.Any("second_revision", second),
	)
	logger.Info("GetChangedTargets: Processing request")

	maxDist := resolveMaxDistance(c.getRepoConfig(first.GetRemote()), request.GetOutputConfig())

	// Try to serve from cache first using the stored treehashes for both revisions.
	if !request.GetBypassCache() {
		if served, err := c.tryServeChangedTargetsFromCache(ctx, logger, scope, stream, request, maxDist); err != nil {
			return err
		} else if served {
			scope.Timer("total_duration").Record(time.Since(start))
			return nil
		}
	}

	graphs, err := c.fetchTwoGraphs(ctx, first, second, request.GetOutputConfig(), request.GetRequestOptions(), request.GetBypassCache())
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return common.WithReason(failureReasonCancelled, common.ErrorTypeUser, err)
		}
		return err
	}
	graphFetchDuration := time.Since(start)
	logger.Info("GetChangedTargets: Both graphs fetched",
		zap.Duration("graph_fetch_duration", graphFetchDuration),
	)
	scope.Timer("graph_fetch_duration").Record(graphFetchDuration)

	compareStart := time.Now()
	responses, err := c.compareTargetGraphs(ctx, logger, graphs[0], graphs[1], maxDist, request.GetOutputConfig().GetComputeDistances())
	// Drop graph references so the GC can reclaim them while the caching goroutine runs.
	graphs[0] = nil
	graphs[1] = nil
	if err != nil {
		logger.Error("GetChangedTargets: Failed to compare target graphs", zap.Error(err))
		return common.WithReason(failureReasonCompare, common.ErrorTypeInfra, fmt.Errorf("failed to compare target graphs: %w", err))
	}
	compareDuration := time.Since(compareStart)
	logger.Info("GetChangedTargets: Target graphs compared",
		zap.Duration("compare_duration", compareDuration),
	)
	scope.Timer("compare_duration").Record(compareDuration)

	// Cache the computed result concurrently so it doesn't block the stream send.
	// Re-read treehashes inside the goroutine — the orchestrator may have stored them
	// during computation. Both the goroutine and the send loop below only read
	// responses, so concurrent access is safe.
	go c.cacheChangedTargetsAsync(logger, first, second, request.GetRequestOptions(), responses)

	sendStart := time.Now()
	if err := sendWithDistanceFilter(stream, responses, maxDist); err != nil {
		logger.Error("GetChangedTargets: Failed to send response", zap.Error(err))
		return common.WithReason(failureReasonSend, common.ErrorTypeInfra, fmt.Errorf("failed to send response: %w", err))
	}
	sendDuration := time.Since(sendStart)
	totalDuration := time.Since(start)
	logger.Info("GetChangedTargets: Successfully processed request",
		zap.Duration("send_duration", sendDuration),
		zap.Duration("total_duration", totalDuration),
	)
	scope.Timer("send_duration").Record(sendDuration)
	scope.Timer("total_duration").Record(totalDuration)
	return nil
}

// tryServeChangedTargetsFromCache returns (true, nil) when a cache hit was
// served end-to-end to the client. (false, nil) means cache miss/corrupt — the
// caller should fall through and recompute. A non-nil error is returned only
// when a downstream send fails after a hit.
func (c *controller) tryServeChangedTargetsFromCache(
	ctx context.Context,
	logger *zap.Logger,
	scope tally.Scope,
	stream pb.TangoServiceGetChangedTargetsYARPCServer,
	request *pb.GetChangedTargetsRequest,
	maxDist int32,
) (bool, error) {
	first, second := request.GetFirstRevision(), request.GetSecondRevision()
	treehash1, treehash2, ok := readTreehashPair(ctx, c.storage, first, second)
	if !ok {
		return false, nil
	}
	cacheStart := time.Now()
	cacheKey := common.GetComparedTargetsCachePath(first.GetRemote(), treehash1, treehash2, request.GetRequestOptions())

	cached, hit := loadCachedResponses(ctx, logger, "GetChangedTargets", func(ctx context.Context) (cacheReader[*pb.GetChangedTargetsResponse], error) {
		return storage.NewChangedTargetsReader(ctx, c.storage, cacheKey)
	})
	if !hit {
		return false, nil
	}
	cacheReadDuration := time.Since(cacheStart)
	logger.Info("GetChangedTargets: Cache hit, streaming from storage",
		zap.Duration("cache_read_duration", cacheReadDuration),
	)
	scope.Counter("cache_hit").Inc(1)
	scope.Timer("cache_read_duration").Record(cacheReadDuration)
	if err := sendWithDistanceFilter(stream, cached, maxDist); err != nil {
		logger.Error("GetChangedTargets: Failed to send cached response", zap.Error(err))
		return false, common.WithReason(failureReasonSend, common.ErrorTypeInfra, fmt.Errorf("failed to send cached response: %w", err))
	}
	logger.Info("GetChangedTargets: Successfully streamed from cache")
	return true, nil
}

// cacheChangedTargetsAsync writes the computed result back to storage. Runs
// in its own goroutine so it cannot block the response stream. Failures are
// logged and otherwise ignored.
func (c *controller) cacheChangedTargetsAsync(
	logger *zap.Logger,
	first, second *pb.BuildDescription,
	requestOptions *pb.RequestOptions,
	responses []*pb.GetChangedTargetsResponse,
) {
	cacheCtx := context.Background()
	treehash1, treehash2, ok := readTreehashPair(cacheCtx, c.storage, first, second)
	if !ok {
		return
	}
	cacheKey := common.GetComparedTargetsCachePath(first.GetRemote(), treehash1, treehash2, requestOptions)
	if err := storage.WriteChangedTargetsStream(cacheCtx, c.storage, cacheKey, responses); err != nil {
		logger.Warn("GetChangedTargets: Failed to cache result", zap.Error(err))
	}
}

// compareTargetGraphs diffs two target graph streams and produces a chunked
// GetChangedTargetsResponse stream. Targets present only on one side are
// classified as NEW or removed; targets present on both sides are classified
// as DIRECT or INDIRECT based on hash, source-file dependencies, direct
// dependency set, and attribute changes. Distances are computed when
// requested or when filtering by distance is active. Output IDs are
// re-mapped into a canonical per-call namespace so the response metadata
// only carries the names actually referenced.
func (c *controller) compareTargetGraphs(
	ctx context.Context,
	logger *zap.Logger,
	firstGraph, secondGraph []*pb.GetTargetGraphResponse,
	maxDist int32,
	outputDistances bool,
) ([]*pb.GetChangedTargetsResponse, error) {
	start := time.Now()
	scope := c.scope.SubScope("compare_target_graphs")
	logger.Info("compareTargetGraphs: Computing differences between target graphs")

	firstByName, firstMetadata, secondByName, secondMetadata, err := indexGraphsByName(ctx, scope, firstGraph, secondGraph)
	if err != nil {
		return nil, err
	}
	// Raw chunk slices are no longer referenced once both graphs are indexed.
	firstGraph = nil
	secondGraph = nil

	sourceFileRuleTypeID := detectSourceFileID(secondMetadata)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	mappers := newIDMappers()
	changedByName := make(map[string]*pb.ChangedTarget, len(secondByName))
	changedSourceFileTargets := make(map[string]struct{})

	diffScanStart := time.Now()
	for name, newT := range secondByName {
		oldT, exists := firstByName[name]
		if !exists {
			changedByName[name] = &pb.ChangedTarget{
				ChangeType: pb.CHANGE_TYPE_NEW,
				NewTarget:  mappers.transpose(newT, secondMetadata),
				Distance:   getDefaultDistance(maxDist, outputDistances, true),
			}
			continue
		}
		if oldT.GetHash() == newT.GetHash() {
			continue
		}
		initial := pb.CHANGE_TYPE_INDIRECT
		// Source-file targets are leaf inputs and any hash change is a direct edit.
		if sourceFileRuleTypeID != -1 && newT.GetRuleType() == sourceFileRuleTypeID {
			initial = pb.CHANGE_TYPE_DIRECT
			changedSourceFileTargets[name] = struct{}{}
		}
		changedByName[name] = &pb.ChangedTarget{
			ChangeType: initial,
			OldTarget:  mappers.transpose(oldT, firstMetadata),
			NewTarget:  mappers.transpose(newT, secondMetadata),
			Distance:   getDefaultDistance(maxDist, outputDistances, false),
		}
	}
	scope.Timer("diff_scan_duration").Record(time.Since(diffScanStart))

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Promote eligible INDIRECT changes to DIRECT.
	classifyStart := time.Now()
	for name, ct := range changedByName {
		if err := promoteToDirectIfNeeded(ct, firstByName[name], secondByName[name], firstMetadata, secondMetadata, changedSourceFileTargets); err != nil {
			return nil, err
		}
	}
	scope.Timer("classify_duration").Record(time.Since(classifyStart))

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Compute BFS distances when filtering is active or the client requested distance output.
	if maxDist >= 0 || outputDistances {
		distancesStart := time.Now()
		if err := computeDistances(ctx, c.logger, changedByName, secondByName, secondMetadata, maxDist); err != nil {
			return nil, err
		}
		scope.Timer("distances_duration").Record(time.Since(distancesStart))
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	responses := c.assembleChangedTargetsResponse(changedByName, mappers)

	totalDuration := time.Since(start)
	logger.Info("compareTargetGraphs: Done", zap.Duration("total_duration", totalDuration))
	scope.Timer("total_duration").Record(totalDuration)
	return responses, nil
}

// indexGraphsByName extracts targets+metadata from both graph streams and
// builds per-revision name->target indexes. Records the index timer on scope.
func indexGraphsByName(
	ctx context.Context,
	scope tally.Scope,
	firstGraph, secondGraph []*pb.GetTargetGraphResponse,
) (map[string]*pb.OptimizedTarget, *pb.Metadata, map[string]*pb.OptimizedTarget, *pb.Metadata, error) {
	indexStart := time.Now()
	firstTargetsByID, firstMetadata, err := getTargetsAndMetadata(ctx, firstGraph)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	secondTargetsByID, secondMetadata, err := getTargetsAndMetadata(ctx, secondGraph)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	firstByName, err := buildNameIndex(ctx, firstTargetsByID, firstMetadata)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	// firstTargetsByID's pointers are now held by firstByName; drop the duplicate map.
	firstTargetsByID = nil
	secondByName, err := buildNameIndex(ctx, secondTargetsByID, secondMetadata)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	secondTargetsByID = nil
	scope.Timer("index_duration").Record(time.Since(indexStart))
	return firstByName, firstMetadata, secondByName, secondMetadata, nil
}

// assembleChangedTargetsResponse turns the in-memory diff state into the
// chunked response stream. Targets are emitted first (always at least one
// envelope, even when there are no changes), followed by chunked metadata.
func (c *controller) assembleChangedTargetsResponse(
	changedByName map[string]*pb.ChangedTarget,
	mappers *idMappers,
) []*pb.GetChangedTargetsResponse {
	changed := make([]*pb.ChangedTarget, 0, len(changedByName))
	for _, ct := range changedByName {
		changed = append(changed, ct)
	}

	var responses []*pb.GetChangedTargetsResponse
	for i := 0; i < len(changed); i += c.changedTargetChunkSize {
		end := min(i+c.changedTargetChunkSize, len(changed))
		responses = append(responses, &pb.GetChangedTargetsResponse{
			Item: &pb.GetChangedTargetsResponse_ChangedTargets{
				ChangedTargets: &pb.ChangedTargets{ChangedTargets: changed[i:end]},
			},
		})
	}
	if len(responses) == 0 {
		responses = append(responses, &pb.GetChangedTargetsResponse{
			Item: &pb.GetChangedTargetsResponse_ChangedTargets{
				ChangedTargets: &pb.ChangedTargets{},
			},
		})
	}
	for _, meta := range mappers.chunkMetadata(c.metadataMapChunkSize) {
		responses = append(responses, &pb.GetChangedTargetsResponse{
			Item: &pb.GetChangedTargetsResponse_Metadata{Metadata: meta},
		})
	}
	return responses
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
			mergeMetadata(merged, item.Metadata)
		}
	}
	return targets, merged, nil
}

// mergeMetadata copies all five name mappings from src into dst, overwriting
// any duplicate keys.
func mergeMetadata(dst, src *pb.Metadata) {
	for k, v := range src.GetTargetIdMapping() {
		dst.TargetIdMapping[k] = v
	}
	for k, v := range src.GetRuleTypeMapping() {
		dst.RuleTypeMapping[k] = v
	}
	for k, v := range src.GetTagMapping() {
		dst.TagMapping[k] = v
	}
	for k, v := range src.GetAttributeNameMapping() {
		dst.AttributeNameMapping[k] = v
	}
	for k, v := range src.GetAttributeStringValueMapping() {
		dst.AttributeStringValueMapping[k] = v
	}
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
			// Targets with no metadata entry are unreferenced; drop them silently.
			continue
		}
		byName[name] = t
	}
	return byName, nil
}

// detectSourceFileID returns the rule-type ID for "source file" when present;
// -1 otherwise. Used to classify hash changes on source-file targets as DIRECT.
func detectSourceFileID(meta *pb.Metadata) int32 {
	if meta == nil {
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

// hasDepInChangedSourceFileTargets returns true if any dependency (resolved via metadata) is a changed source file target.
func hasDepInChangedSourceFileTargets(depIds []int32, meta *pb.Metadata, changedSourceFileTargets map[string]struct{}) bool {
	if meta == nil {
		return false
	}
	idMapping := meta.GetTargetIdMapping()
	for _, id := range depIds {
		name := idMapping[id]
		if name == "" {
			continue
		}
		if _, ok := changedSourceFileTargets[name]; ok {
			return true
		}
	}
	return false
}

// dependenciesChanged checks if the set of direct dependencies changed between old and new targets.
func dependenciesChanged(oldTarget *pb.OptimizedTarget, oldMeta *pb.Metadata, newTarget *pb.OptimizedTarget, newMeta *pb.Metadata) (bool, error) {
	if oldMeta == nil || newMeta == nil {
		return false, nil
	}

	oldDepIDs := oldTarget.GetDirectDependencies()
	newDepIDs := newTarget.GetDirectDependencies()
	if len(oldDepIDs) != len(newDepIDs) {
		return true, nil
	}
	if len(oldDepIDs) == 0 {
		return false, nil
	}
	if err := validateTargetNames(oldTarget, newTarget, oldMeta, newMeta); err != nil {
		return false, fmt.Errorf("target names are different")
	}

	oldTargetIDMapping := oldMeta.GetTargetIdMapping()
	newTargetIDMapping := newMeta.GetTargetIdMapping()
	newDepSet := make(map[string]struct{}, len(newDepIDs))
	for _, depID := range newDepIDs {
		if name := newTargetIDMapping[depID]; name != "" {
			newDepSet[name] = struct{}{}
		}
	}
	for _, depID := range oldDepIDs {
		if name := oldTargetIDMapping[depID]; name != "" {
			if _, exists := newDepSet[name]; !exists {
				return true, nil
			}
		}
	}
	return false, nil
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
	for nameID, valID := range newAttrIDs {
		if attrName := newAttrNameMapping[nameID]; attrName != "" {
			newAttrMap[attrName] = newAttrValMapping[valID]
		}
	}
	for nameID, valID := range oldAttrIDs {
		attrName := oldAttrNameMapping[nameID]
		if attrName == "" {
			continue
		}
		newVal, exists := newAttrMap[attrName]
		if !exists || newVal != oldAttrValMapping[valID] {
			return true, nil
		}
	}
	return false, nil
}

// validateTargetNames checks if the target names are the same between old and new targets, and exists in both metadata maps.
func validateTargetNames(oldTarget, newTarget *pb.OptimizedTarget, oldMeta, newMeta *pb.Metadata) error {
	oldName, ok := oldMeta.GetTargetIdMapping()[oldTarget.GetId()]
	if !ok {
		return fmt.Errorf("old target id %d not found in metadata", oldTarget.GetId())
	}
	newName, ok := newMeta.GetTargetIdMapping()[newTarget.GetId()]
	if !ok {
		return fmt.Errorf("new target id %d not found in metadata", newTarget.GetId())
	}
	if oldName != newName {
		return fmt.Errorf("target names are different %s != %s", oldName, newName)
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
	if deps := src.GetDirectDependencies(); len(deps) > 0 {
		out := make([]int32, 0, len(deps))
		for _, d := range deps {
			out = append(out, getTargetId(oldTargetIdMap[d]))
		}
		dst.DirectDependencies = out
	}
	if rtName := oldRuleTypeIdMap[src.GetRuleType()]; rtName != "" {
		dst.RuleType = getRuleTypeId(rtName)
	}
	if tags := src.GetTags(); len(tags) > 0 {
		out := make([]int32, 0, len(tags))
		for _, tg := range tags {
			out = append(out, getTagId(oldTagIdMap[tg]))
		}
		dst.Tags = out
	}
	if attrs := src.GetAttributes(); len(attrs) > 0 {
		out := make(map[int32]int32, len(attrs))
		for k, v := range attrs {
			out[getAttrNameId(attrNameIdMap[k])] = getAttrValId(attrValIdMap[v])
		}
		dst.Attributes = out
	}
	return dst
}

// sendWithDistanceFilter streams responses to the client, filtering changed targets to those
// within maxDist from any CHANGE_TYPE_DIRECT target when maxDist >= 0.
// Metadata and other non-target responses are always forwarded.
// Filtering and sending are combined into a single pass to avoid an intermediate allocation.
func sendWithDistanceFilter(stream pb.TangoServiceGetChangedTargetsYARPCServer, responses []*pb.GetChangedTargetsResponse, maxDist int32) error {
	for _, resp := range responses {
		toSend := resp
		if maxDist >= 0 {
			if ct, ok := resp.GetItem().(*pb.GetChangedTargetsResponse_ChangedTargets); ok {
				kept := filterChangedTargetsByDistance(ct.ChangedTargets.GetChangedTargets(), maxDist)
				toSend = &pb.GetChangedTargetsResponse{
					Item: &pb.GetChangedTargetsResponse_ChangedTargets{
						ChangedTargets: &pb.ChangedTargets{ChangedTargets: kept},
					},
				}
			}
		}
		if err := stream.Send(toSend); err != nil {
			return err
		}
	}
	return nil
}

// computeDistances computes the shortest distance from any CHANGE_TYPE_DIRECT
// target to each changed target via the reverse dependency graph using BFS.
// DIRECT targets get distance 0, their reverse dependants get 1, and so on.
// When maxDistance >= 0, the BFS is pruned: targets at distance > maxDistance are never
// enqueued, so they keep their initial distance of -1 (out-of-range).
//
// Targets unreachable from any DIRECT target keep the initial distance of -1.
func computeDistances(ctx context.Context, logger *zap.Logger, changedByName map[string]*pb.ChangedTarget, targetsByName map[string]*pb.OptimizedTarget, meta *pb.Metadata, maxDistance int32) error {
	if meta == nil {
		return nil
	}
	reverseDeps, err := buildReverseDeps(ctx, targetsByName, meta)
	if err != nil {
		return err
	}

	queue, visited := seedBFSFromDirectChanges(changedByName)

	iter := 0
	for len(queue) > 0 {
		if iter%cancelCheckInterval == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		iter++
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

	// Warn about INDIRECT targets with no path to a DIRECT change — likely a hashing bug.
	for name, ct := range changedByName {
		if ct.GetChangeType() == pb.CHANGE_TYPE_INDIRECT && ct.GetDistance() == -1 {
			logger.Warn("computeDistances: INDIRECT target has no path to a DIRECT change, possible hashing issue",
				zap.String("target", name),
			)
		}
	}
	return nil
}

// buildReverseDeps inverts the target dependency graph: if B depends on A,
// reverseDeps[A] contains B. Used as the adjacency list for distance BFS.
func buildReverseDeps(ctx context.Context, targetsByName map[string]*pb.OptimizedTarget, meta *pb.Metadata) (map[string][]string, error) {
	idMapping := meta.GetTargetIdMapping()
	reverseDeps := make(map[string][]string, len(targetsByName))
	iter := 0
	for name, t := range targetsByName {
		if iter%cancelCheckInterval == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		iter++
		for _, depID := range t.GetDirectDependencies() {
			if depName := idMapping[depID]; depName != "" {
				reverseDeps[depName] = append(reverseDeps[depName], name)
			}
		}
	}
	return reverseDeps, nil
}

// seedBFSFromDirectChanges initializes the BFS queue with DIRECT/NEW changes
// (distance 0) and resets every other distance to -1.
func seedBFSFromDirectChanges(changedByName map[string]*pb.ChangedTarget) ([]string, map[string]struct{}) {
	var queue []string
	visited := make(map[string]struct{}, len(changedByName))
	for name, ct := range changedByName {
		if ct.GetChangeType() == pb.CHANGE_TYPE_DIRECT || ct.GetChangeType() == pb.CHANGE_TYPE_NEW {
			ct.Distance = 0
			queue = append(queue, name)
			visited[name] = struct{}{}
		} else {
			ct.Distance = -1
		}
	}
	return queue, visited
}

// validateGetChangedTargetsRequest validates the GetChangedTargetsRequest by
// delegating revision invariants to validateRevisionPair.
func validateGetChangedTargetsRequest(request *pb.GetChangedTargetsRequest) error {
	if request == nil {
		return errors.New("request cannot be nil")
	}
	return validateRevisionPair(request.GetFirstRevision(), request.GetSecondRevision())
}

// getDefaultDistance picks the initial Distance value to store on a freshly
// classified ChangedTarget. -1 is used when distances are neither requested
// nor needed for filtering, so callers can cheaply skip BFS entirely. NEW
// targets always start at 0 because they are their own seeds.
func getDefaultDistance(maxDist int32, outputDistances bool, forNewTarget bool) int32 {
	if maxDist < 0 && !outputDistances {
		return -1
	}
	if forNewTarget {
		return 0
	}
	return -1
}
