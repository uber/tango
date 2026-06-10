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

// edgeKey identifies an edge by source and dependency target names.
// Used only by buildEdgeSet and its tests; compareTargetGraphsAndEdges uses
// packed uint64 keys for lower memory overhead.
type edgeKey struct{ source, dep string }

// packEdge packs two int32 IDs into a uint64 for use as a compact, allocation-free map key.
func packEdge(src, dep int32) uint64 {
	return uint64(uint32(src))<<32 | uint64(uint32(dep))
}

// unpackEdge inverts packEdge.
func unpackEdge(packed uint64) (src, dep int32) {
	return int32(packed >> 32), int32(packed & 0xFFFFFFFF)
}

// GetChangedTargetsAndEdges returns the changed targets and edges between two revisions.
func (c *controller) GetChangedTargetsAndEdges(request *pb.GetChangedTargetsAndEdgesRequest, stream pb.TangoServiceGetChangedTargetsAndEdgesYARPCServer) (retErr error) {
	scope := c.scope.SubScope("get_changed_targets_and_edges")
	scope.Counter("calls").Inc(1)
	defer recordRPCResult(scope, &retErr)

	if err := validateGetChangedTargetsAndEdgesRequest(request); err != nil {
		c.logger.Error("GetChangedTargetsAndEdges: Invalid request", zap.Error(err))
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
	logger.Info("GetChangedTargetsAndEdges: Processing request")

	maxDist := resolveMaxDistance(c.getRepoConfig(first.GetRemote()), request.GetOutputConfig())

	if !request.GetBypassCache() {
		if served, err := c.tryServeChangedTargetsAndEdgesFromCache(ctx, logger, scope, stream, request, maxDist); err != nil {
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
	logger.Info("GetChangedTargetsAndEdges: Both graphs fetched",
		zap.Duration("graph_fetch_duration", graphFetchDuration),
	)
	scope.Timer("graph_fetch_duration").Record(graphFetchDuration)

	compareStart := time.Now()
	responses, err := c.compareTargetGraphsAndEdges(ctx, logger, graphs[0], graphs[1], maxDist, request.GetOutputConfig().GetComputeDistances())
	graphs[0] = nil
	graphs[1] = nil
	if err != nil {
		logger.Error("GetChangedTargetsAndEdges: Failed to compare target graphs", zap.Error(err))
		return common.WithReason(failureReasonCompare, common.ErrorTypeInfra, fmt.Errorf("failed to compare target graphs: %w", err))
	}
	compareDuration := time.Since(compareStart)
	logger.Info("GetChangedTargetsAndEdges: Target graphs compared",
		zap.Duration("compare_duration", compareDuration),
	)
	scope.Timer("compare_duration").Record(compareDuration)

	go c.cacheChangedTargetsAndEdgesAsync(logger, first, second, request.GetRequestOptions(), responses)

	sendStart := time.Now()
	if err := sendWithDistanceFilterForEdges(stream, responses, maxDist); err != nil {
		logger.Error("GetChangedTargetsAndEdges: Failed to send response", zap.Error(err))
		return common.WithReason(failureReasonSend, common.ErrorTypeInfra, err)
	}
	sendDuration := time.Since(sendStart)
	totalDuration := time.Since(start)
	logger.Info("GetChangedTargetsAndEdges: Successfully processed request",
		zap.Duration("send_duration", sendDuration),
		zap.Duration("total_duration", totalDuration),
	)
	scope.Timer("send_duration").Record(sendDuration)
	scope.Timer("total_duration").Record(totalDuration)
	return nil
}

// tryServeChangedTargetsAndEdgesFromCache returns (true, nil) on a hit served
// end-to-end. (false, nil) means the caller should fall through and recompute.
func (c *controller) tryServeChangedTargetsAndEdgesFromCache(
	ctx context.Context,
	logger *zap.Logger,
	scope tally.Scope,
	stream pb.TangoServiceGetChangedTargetsAndEdgesYARPCServer,
	request *pb.GetChangedTargetsAndEdgesRequest,
	maxDist int32,
) (bool, error) {
	first, second := request.GetFirstRevision(), request.GetSecondRevision()
	treehash1, treehash2, ok := readTreehashPair(ctx, c.storage, first, second)
	if !ok {
		return false, nil
	}
	cacheStart := time.Now()
	cacheKey := common.GetChangedTargetsAndEdgesCachePath(first.GetRemote(), treehash1, treehash2, request.GetRequestOptions())

	cached, hit := loadCachedResponses(ctx, logger, "GetChangedTargetsAndEdges", func(ctx context.Context) (cacheReader[*pb.GetChangedTargetsAndEdgesResponse], error) {
		return storage.NewChangedTargetsAndEdgesReader(ctx, c.storage, cacheKey)
	})
	if !hit {
		return false, nil
	}
	cacheReadDuration := time.Since(cacheStart)
	logger.Info("GetChangedTargetsAndEdges: Cache hit, streaming from storage",
		zap.Duration("cache_read_duration", cacheReadDuration),
	)
	scope.Counter("cache_hit").Inc(1)
	scope.Timer("cache_read_duration").Record(cacheReadDuration)
	if err := sendWithDistanceFilterForEdges(stream, cached, maxDist); err != nil {
		logger.Error("GetChangedTargetsAndEdges: Failed to send cached response", zap.Error(err))
		return false, common.WithReason(failureReasonSend, common.ErrorTypeInfra, err)
	}
	logger.Info("GetChangedTargetsAndEdges: Successfully streamed from cache")
	return true, nil
}

// cacheChangedTargetsAndEdgesAsync writes the computed result back to storage
// without blocking the response stream. Failures are logged and ignored.
func (c *controller) cacheChangedTargetsAndEdgesAsync(
	logger *zap.Logger,
	first, second *pb.BuildDescription,
	requestOptions *pb.RequestOptions,
	responses []*pb.GetChangedTargetsAndEdgesResponse,
) {
	cacheCtx := context.Background()
	treehash1, treehash2, ok := readTreehashPair(cacheCtx, c.storage, first, second)
	if !ok {
		return
	}
	cacheKey := common.GetChangedTargetsAndEdgesCachePath(first.GetRemote(), treehash1, treehash2, requestOptions)
	if err := storage.WriteChangedTargetsAndEdgesStream(cacheCtx, c.storage, cacheKey, responses); err != nil {
		logger.Warn("GetChangedTargetsAndEdges: Failed to cache result", zap.Error(err))
	}
}

// compareTargetGraphsAndEdges diffs two target graph streams and produces a
// chunked GetChangedTargetsAndEdgesResponse stream. In addition to the
// per-target classification done by compareTargetGraphs, it tracks
// per-target topology by computing added/removed targets and the set of
// new and removed edges. Edge keys are packed into uint64 ID pairs to keep
// the working set small for very large graphs.
func (c *controller) compareTargetGraphsAndEdges(
	ctx context.Context,
	logger *zap.Logger,
	firstGraph, secondGraph []*pb.GetTargetGraphResponse,
	maxDist int32,
	outputDistances bool,
) ([]*pb.GetChangedTargetsAndEdgesResponse, error) {
	start := time.Now()
	scope := c.scope.SubScope("compare_target_graphs_and_edges")
	logger.Info("compareTargetGraphsAndEdges: Computing differences between target graphs")

	firstByName, firstMetadata, secondByName, secondMetadata, err := indexGraphsByName(ctx, scope, firstGraph, secondGraph)
	if err != nil {
		return nil, err
	}
	// Raw chunk slices are no longer referenced once both graphs are indexed.
	firstGraph = nil
	secondGraph = nil

	sourceFileRuleTypeID := detectSourceFileID(secondMetadata)

	mappers := newIDMappers()
	// edgeMapper assigns compact int32 IDs for edge comparison only. Kept separate
	// from the output mappers so response metadata is not polluted with every
	// target name in the graph.
	edgeMapper := common.NewNameIDMapper()

	changedByName := make(map[string]*pb.ChangedTarget)
	changedSourceFileTargets := make(map[string]struct{})
	var addedTargets []*pb.OptimizedTarget
	secondEdges := make(map[uint64]struct{})

	// Single pass over the second graph: build edges, identify changed/new targets.
	if err := scanSecondGraph(ctx, secondByName, firstByName, secondMetadata, sourceFileRuleTypeID,
		mappers, firstMetadata, edgeMapper, maxDist, outputDistances,
		changedByName, changedSourceFileTargets, secondEdges, &addedTargets); err != nil {
		return nil, err
	}

	// Promote eligible INDIRECT changes to DIRECT.
	classifyIter := 0
	for name, ct := range changedByName {
		if classifyIter%cancelCheckInterval == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		classifyIter++
		if err := promoteToDirectIfNeeded(ct, firstByName[name], secondByName[name], firstMetadata, secondMetadata, changedSourceFileTargets); err != nil {
			return nil, err
		}
	}

	if maxDist >= 0 || outputDistances {
		if err := computeDistances(ctx, logger, changedByName, secondByName, secondMetadata, maxDist); err != nil {
			return nil, err
		}
	}

	// Single pass over the first graph: collect removed targets and build the first edge set.
	firstEdges := make(map[uint64]struct{})
	var removedTargets []*pb.OptimizedTarget
	if err := scanFirstGraph(ctx, firstByName, secondByName, firstMetadata, mappers, edgeMapper, firstEdges, &removedTargets); err != nil {
		return nil, err
	}

	newEdges, removedEdges, err := diffEdges(ctx, secondEdges, firstEdges, edgeMapper, mappers)
	if err != nil {
		return nil, err
	}
	// Drop intermediates eagerly so the response assembly works with a smaller heap.
	secondEdges = nil
	firstEdges = nil

	totalDuration := time.Since(start)
	logger.Info("compareTargetGraphsAndEdges: Done", zap.Duration("total_duration", totalDuration))
	scope.Timer("total_duration").Record(totalDuration)

	return c.assembleChangedTargetsAndEdgesResponse(changedByName, addedTargets, removedTargets, newEdges, removedEdges, mappers), nil
}

// scanSecondGraph builds the second edge set and classifies each target as
// NEW, INDIRECT (default), DIRECT (when the rule type is source file), or
// unchanged. Pointers to addedTargets and the result maps are mutated in place.
func scanSecondGraph(
	ctx context.Context,
	secondByName, firstByName map[string]*pb.OptimizedTarget,
	secondMetadata *pb.Metadata,
	sourceFileRuleTypeID int32,
	mappers *idMappers,
	firstMetadata *pb.Metadata,
	edgeMapper *common.NameIDMapper,
	maxDist int32,
	outputDistances bool,
	changedByName map[string]*pb.ChangedTarget,
	changedSourceFileTargets map[string]struct{},
	secondEdges map[uint64]struct{},
	addedTargets *[]*pb.OptimizedTarget,
) error {
	secondIDMapping := secondMetadata.GetTargetIdMapping()
	iter := 0
	for name, newT := range secondByName {
		if iter%cancelCheckInterval == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		iter++
		for _, depID := range newT.GetDirectDependencies() {
			if depName := secondIDMapping[depID]; depName != "" {
				secondEdges[packEdge(edgeMapper.ID(name), edgeMapper.ID(depName))] = struct{}{}
			}
		}

		oldT, exists := firstByName[name]
		if !exists {
			transposed := mappers.transpose(newT, secondMetadata)
			changedByName[name] = &pb.ChangedTarget{
				ChangeType: pb.CHANGE_TYPE_NEW,
				NewTarget:  transposed,
				Distance:   getDefaultDistance(maxDist, outputDistances, true),
			}
			*addedTargets = append(*addedTargets, transposed)
			continue
		}
		if oldT.GetHash() == newT.GetHash() {
			continue
		}
		initial := pb.CHANGE_TYPE_INDIRECT
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
	return nil
}

// scanFirstGraph builds the first edge set and collects targets present in
// the first revision but missing from the second (i.e. removed).
func scanFirstGraph(
	ctx context.Context,
	firstByName, secondByName map[string]*pb.OptimizedTarget,
	firstMetadata *pb.Metadata,
	mappers *idMappers,
	edgeMapper *common.NameIDMapper,
	firstEdges map[uint64]struct{},
	removedTargets *[]*pb.OptimizedTarget,
) error {
	firstIDMapping := firstMetadata.GetTargetIdMapping()
	iter := 0
	for name, oldT := range firstByName {
		if iter%cancelCheckInterval == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		iter++
		for _, depID := range oldT.GetDirectDependencies() {
			if depName := firstIDMapping[depID]; depName != "" {
				firstEdges[packEdge(edgeMapper.ID(name), edgeMapper.ID(depName))] = struct{}{}
			}
		}
		if _, exists := secondByName[name]; !exists {
			*removedTargets = append(*removedTargets, mappers.transpose(oldT, firstMetadata))
		}
	}
	return nil
}

// diffEdges returns (added, removed) edge lists from the two packed edge sets.
// Packed IDs are resolved back to names via edgeMapper, then re-projected
// through the output target mapper so edge endpoints share the response
// namespace.
func diffEdges(
	ctx context.Context,
	secondEdges, firstEdges map[uint64]struct{},
	edgeMapper *common.NameIDMapper,
	mappers *idMappers,
) (newEdges, removedEdges []*pb.Edge, err error) {
	edgeNames := edgeMapper.Invert()

	if newEdges, err = collectEdgeDelta(ctx, secondEdges, firstEdges, edgeNames, mappers); err != nil {
		return nil, nil, err
	}
	if removedEdges, err = collectEdgeDelta(ctx, firstEdges, secondEdges, edgeNames, mappers); err != nil {
		return nil, nil, err
	}
	return newEdges, removedEdges, nil
}

// collectEdgeDelta returns edges in `from` that are not in `to`, projected
// into the output target ID namespace.
func collectEdgeDelta(
	ctx context.Context,
	from, to map[uint64]struct{},
	edgeNames map[int32]string,
	mappers *idMappers,
) ([]*pb.Edge, error) {
	var out []*pb.Edge
	iter := 0
	for e := range from {
		if iter%cancelCheckInterval == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		iter++
		if _, exists := to[e]; exists {
			continue
		}
		srcID, depID := unpackEdge(e)
		out = append(out, &pb.Edge{
			SourceId: mappers.targets.ID(edgeNames[srcID]),
			TargetId: mappers.targets.ID(edgeNames[depID]),
		})
	}
	return out, nil
}

// assembleChangedTargetsAndEdgesResponse chunks targets/edges and appends the
// canonical metadata. Edges are tiny and always fit in a single envelope.
func (c *controller) assembleChangedTargetsAndEdgesResponse(
	changedByName map[string]*pb.ChangedTarget,
	addedTargets, removedTargets []*pb.OptimizedTarget,
	newEdges, removedEdges []*pb.Edge,
	mappers *idMappers,
) []*pb.GetChangedTargetsAndEdgesResponse {
	changed := make([]*pb.ChangedTarget, 0, len(changedByName))
	for _, ct := range changedByName {
		changed = append(changed, ct)
	}

	var responses []*pb.GetChangedTargetsAndEdgesResponse
	appendCTEPayload := func(p *pb.ChangedTargetsAndEdges) {
		responses = append(responses, &pb.GetChangedTargetsAndEdgesResponse{
			Item: &pb.GetChangedTargetsAndEdgesResponse_ChangedTargetsAndEdges{ChangedTargetsAndEdges: p},
		})
	}

	for i := 0; i < len(changed); i += c.changedTargetChunkSize {
		end := min(i+c.changedTargetChunkSize, len(changed))
		appendCTEPayload(&pb.ChangedTargetsAndEdges{ChangedTargets: changed[i:end]})
	}
	for i := 0; i < len(addedTargets); i += c.targetChunkSize {
		end := min(i+c.targetChunkSize, len(addedTargets))
		appendCTEPayload(&pb.ChangedTargetsAndEdges{AddedTargets: addedTargets[i:end]})
	}
	for i := 0; i < len(removedTargets); i += c.targetChunkSize {
		end := min(i+c.targetChunkSize, len(removedTargets))
		appendCTEPayload(&pb.ChangedTargetsAndEdges{RemovedTargets: removedTargets[i:end]})
	}
	if len(newEdges) > 0 || len(removedEdges) > 0 {
		appendCTEPayload(&pb.ChangedTargetsAndEdges{NewEdges: newEdges, RemovedEdges: removedEdges})
	}
	if len(responses) == 0 {
		// Emit an empty chunk when there are no changes at all, so the stream is never empty.
		appendCTEPayload(&pb.ChangedTargetsAndEdges{})
	}
	for _, meta := range mappers.chunkMetadata(c.metadataMapChunkSize) {
		responses = append(responses, &pb.GetChangedTargetsAndEdgesResponse{
			Item: &pb.GetChangedTargetsAndEdgesResponse_Metadata{Metadata: meta},
		})
	}
	return responses
}

// buildEdgeSet constructs a set of (source, dep) name pairs from all direct dependencies in the graph.
func buildEdgeSet(byName map[string]*pb.OptimizedTarget, meta *pb.Metadata) map[edgeKey]struct{} {
	if meta == nil {
		return nil
	}
	idMapping := meta.GetTargetIdMapping()
	edges := make(map[edgeKey]struct{})
	for source, t := range byName {
		for _, depID := range t.GetDirectDependencies() {
			depName := idMapping[depID]
			if depName != "" {
				edges[edgeKey{source: source, dep: depName}] = struct{}{}
			}
		}
	}
	return edges
}

// sendWithDistanceFilterForEdges streams responses, filtering changed_targets by
// BFS distance when maxDist >= 0. Added/removed targets and edges pass through
// unchanged — they represent graph topology deltas that are not ranked by
// distance from a CHANGE_TYPE_DIRECT seed.
func sendWithDistanceFilterForEdges(
	stream pb.TangoServiceGetChangedTargetsAndEdgesYARPCServer,
	responses []*pb.GetChangedTargetsAndEdgesResponse,
	maxDist int32,
) error {
	for _, resp := range responses {
		toSend := resp
		if maxDist >= 0 {
			if cte, ok := resp.GetItem().(*pb.GetChangedTargetsAndEdgesResponse_ChangedTargetsAndEdges); ok {
				payload := cte.ChangedTargetsAndEdges
				toSend = &pb.GetChangedTargetsAndEdgesResponse{
					Item: &pb.GetChangedTargetsAndEdgesResponse_ChangedTargetsAndEdges{
						ChangedTargetsAndEdges: &pb.ChangedTargetsAndEdges{
							ChangedTargets: filterChangedTargetsByDistance(payload.GetChangedTargets(), maxDist),
							AddedTargets:   payload.GetAddedTargets(),
							RemovedTargets: payload.GetRemovedTargets(),
							NewEdges:       payload.GetNewEdges(),
							RemovedEdges:   payload.GetRemovedEdges(),
						},
					},
				}
			}
		}
		if err := stream.Send(toSend); err != nil {
			return fmt.Errorf("failed to send response: %w", err)
		}
	}
	return nil
}

// validateGetChangedTargetsAndEdgesRequest enforces the same invariants as
// validateGetChangedTargetsRequest by delegating to validateRevisionPair.
func validateGetChangedTargetsAndEdgesRequest(request *pb.GetChangedTargetsAndEdgesRequest) error {
	if request == nil {
		return errors.New("request cannot be nil")
	}
	return validateRevisionPair(request.GetFirstRevision(), request.GetSecondRevision())
}
