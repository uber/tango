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
	"io"
	"time"

	"github.com/uber/tango/core/common"
	"github.com/uber/tango/core/storage"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/multierr"
	"go.uber.org/zap"
)

// edgeKey identifies an edge by source and dependency target names.
type edgeKey struct{ source, dep string }

// GetChangedTargetsAndEdges returns the changed targets and edges between two revisions.
func (c *controller) GetChangedTargetsAndEdges(request *pb.GetChangedTargetsAndEdgesRequest, stream pb.TangoServiceGetChangedTargetsAndEdgesYARPCServer) error {
	if err := validateGetChangedTargetsAndEdgesRequest(request); err != nil {
		c.logger.Error("GetChangedTargetsAndEdges: Invalid request", zap.Error(err))
		return err
	}
	ctx := stream.Context()
	start := time.Now()
	scope := c.scope.SubScope("get_changed_targets_and_edges")
	logger := c.logger.With(
		zap.Any("first_revision", request.GetFirstRevision()),
		zap.Any("second_revision", request.GetSecondRevision()),
	)

	logger.Info("GetChangedTargetsAndEdges: Processing request")

	// Try to serve from cache first.
	cacheStart := time.Now()
	treehash1 := readTreehash(ctx, c.storage, request.GetFirstRevision())
	treehash2 := readTreehash(ctx, c.storage, request.GetSecondRevision())
	if treehash1 != "" && treehash2 != "" {
		cacheKey := common.GetChangedTargetsAndEdgesCachePath(request.GetFirstRevision().GetRemote(), treehash1, treehash2)
		cachedReader, cacheErr := storage.NewChangedTargetsAndEdgesReader(ctx, c.storage, cacheKey)
		if cacheErr != nil && !storage.IsNotFound(cacheErr) {
			c.logger.Warn("GetChangedTargetsAndEdges: Failed to read from cache, proceeding to compute", zap.Error(cacheErr))
		} else if cachedReader != nil {
			var cached []*pb.GetChangedTargetsAndEdgesResponse
			var readErr error
			for {
				var resp *pb.GetChangedTargetsAndEdgesResponse
				resp, readErr = cachedReader.Read()
				if readErr == io.EOF {
					readErr = nil
					break
				}
				if readErr != nil {
					break
				}
				cached = append(cached, resp)
			}
			cachedReader.Close()

			if readErr != nil {
				c.logger.Warn("GetChangedTargetsAndEdges: Cached result is incomplete, recomputing", zap.Error(readErr))
			} else {
				cacheReadDuration := time.Since(cacheStart)
				c.logger.Info("GetChangedTargetsAndEdges: Cache hit, streaming from storage",
					zap.Duration("cache_read_duration", cacheReadDuration),
				)
				scope.Counter("cache_hit").Inc(1)
				scope.Timer("cache_read_duration").Record(cacheReadDuration)
				for _, resp := range cached {
					if err := stream.Send(resp); err != nil {
						c.logger.Error("GetChangedTargetsAndEdges: Failed to send cached response", zap.Error(err))
						return fmt.Errorf("failed to send cached response: %w", err)
					}
				}
				totalDuration := time.Since(start)
				c.logger.Info("GetChangedTargetsAndEdges: Successfully streamed from cache",
					zap.Duration("total_duration", totalDuration),
				)
				scope.Timer("total_duration").Record(totalDuration)
				return nil
			}
		}
	}

	jobs := make([]*job, 2)
	for i := 0; i < 2; i++ {
		ctxNew, cancelNew := context.WithCancel(ctx)
		defer cancelNew()
		jobs[i] = &job{ctx: ctxNew, cancel: cancelNew}
	}

	type graphResult struct {
		order  int
		chunks []*pb.GetTargetGraphResponse
		err    error
	}
	results := make(chan graphResult, len(jobs))
	graphFetchStart := time.Now()

	for i := 0; i < len(jobs); i++ {
		i := i
		go func(idx int) {
			defer func() {
				if r := recover(); r != nil {
					results <- graphResult{order: idx, err: fmt.Errorf("panic in graph fetch: %v", r)}
				}
			}()
			var revision *pb.BuildDescription
			if idx == 0 {
				revision = request.GetFirstRevision()
			} else {
				revision = request.GetSecondRevision()
			}
			graphReader, err := c.getGraph(jobs[idx].ctx, revision, request.GetOutputConfig())
			if err != nil || graphReader == nil {
				results <- graphResult{order: idx, err: err}
				return
			}
			defer graphReader.Close()

			var chunks []*pb.GetTargetGraphResponse
			for {
				chunk, err := graphReader.Read()
				if err == io.EOF {
					results <- graphResult{order: idx, chunks: chunks}
					return
				}
				if err != nil {
					results <- graphResult{order: idx, err: err}
					return
				}
				chunks = append(chunks, chunk)
			}
		}(i)
	}

	for range jobs {
		select {
		case res := <-results:
			jobs[res.order].graphStreamChunks = res.chunks
			jobs[res.order].completed = true
			jobs[res.order].err = res.err
			if res.chunks == nil && res.err == nil {
				jobs[res.order].err = errors.New("no chunks returned")
			}
			if jobs[res.order].err != nil {
				other := (res.order + 1) % 2
				if !jobs[other].completed {
					jobs[other].cancel()
					jobs[other].cancelled = true
				}
			}
		}
	}

	graphFetchDuration := time.Since(graphFetchStart)
	c.logger.Info("GetChangedTargetsAndEdges: Both graphs fetched",
		zap.Duration("graph_fetch_duration", graphFetchDuration),
	)
	scope.Timer("graph_fetch_duration").Record(graphFetchDuration)

	if ctx.Err() != nil {
		return ctx.Err()
	}

	var err error
	for i, j := range jobs {
		if j.err != nil {
			if j.cancelled {
				continue
			}
			err = multierr.Append(err, fmt.Errorf("failed to get target graph #%d: %w", i+1, j.err))
		}
	}
	if err != nil {
		return err
	}

	firstGraph := jobs[0].graphStreamChunks
	secondGraph := jobs[1].graphStreamChunks

	compareStart := time.Now()
	responses, err := c.compareTargetGraphsAndEdges(ctx, firstGraph, secondGraph, request.GetOutputConfig())
	if err != nil {
		c.logger.Error("GetChangedTargetsAndEdges: Failed to compare target graphs", zap.Error(err))
		return fmt.Errorf("failed to compare target graphs: %w", err)
	}
	compareDuration := time.Since(compareStart)
	c.logger.Info("GetChangedTargetsAndEdges: Target graphs compared",
		zap.Duration("compare_duration", compareDuration),
	)
	scope.Timer("compare_duration").Record(compareDuration)

	// Cache the computed result concurrently so it doesn't block the stream send.
	go func() {
		cacheCtx := context.Background()
		treehash1 := readTreehash(cacheCtx, c.storage, request.GetFirstRevision())
		treehash2 := readTreehash(cacheCtx, c.storage, request.GetSecondRevision())
		if treehash1 != "" && treehash2 != "" {
			cacheKey := common.GetChangedTargetsAndEdgesCachePath(request.GetFirstRevision().GetRemote(), treehash1, treehash2)
			if writeErr := storage.WriteChangedTargetsAndEdgesStream(cacheCtx, c.storage, cacheKey, responses); writeErr != nil {
				c.logger.Warn("GetChangedTargetsAndEdges: Failed to cache result", zap.Error(writeErr))
			}
		}
	}()

	for _, resp := range responses {
		if err := stream.Send(resp); err != nil {
			c.logger.Error("GetChangedTargetsAndEdges: Failed to send response", zap.Error(err))
			return fmt.Errorf("failed to send response: %w", err)
		}
	}

	totalDuration := time.Since(start)
	c.logger.Info("GetChangedTargetsAndEdges: Successfully processed request",
		zap.Duration("total_duration", totalDuration),
	)
	scope.Timer("total_duration").Record(totalDuration)
	return nil
}

func (c *controller) compareTargetGraphsAndEdges(ctx context.Context, firstGraph, secondGraph []*pb.GetTargetGraphResponse, outputConfig *pb.OutputConfig) ([]*pb.GetChangedTargetsAndEdgesResponse, error) {
	start := time.Now()
	scope := c.scope.SubScope("compare_target_graphs_and_edges")
	c.logger.Info("compareTargetGraphsAndEdges: Computing differences between target graphs")

	// 1) Extract targets and metadata; index by canonical names.
	firstTargetsByID, firstMetadata := getTargetsAndMetadata(firstGraph)
	secondTargetsByID, secondMetadata := getTargetsAndMetadata(secondGraph)
	firstByName := buildNameIndex(firstTargetsByID, firstMetadata)
	secondByName := buildNameIndex(secondTargetsByID, secondMetadata)

	sourceFileRuleTypeID := detectSourceFileID(secondMetadata)

	// 2) Create canonical ID mappers shared across all output fields.
	targetMapper := common.NewNameIDMapper()
	ruleTypeMapper := common.NewNameIDMapper()
	tagMapper := common.NewNameIDMapper()
	attrNameMapper := common.NewNameIDMapper()
	attrValMapper := common.NewNameIDMapper()
	getTargetId := func(name string) int32 { return targetMapper.ID(name) }
	getRuleTypeId := func(name string) int32 { return ruleTypeMapper.ID(name) }
	getTagId := func(name string) int32 { return tagMapper.ID(name) }
	getAttrNameId := func(name string) int32 { return attrNameMapper.ID(name) }
	getAttrValId := func(name string) int32 { return attrValMapper.ID(name) }

	changedByName := make(map[string]*pb.ChangedTarget)
	changedSourceFileTargets := make(map[string]struct{})
	var addedTargets []*pb.OptimizedTarget
	secondIDMapping := secondMetadata.GetTargetIdMapping()
	secondEdges := make(map[edgeKey]struct{})

	// 3) Single pass over second graph: identify changed/new/added targets and build second edge set.
	for name, newT := range secondByName {
		// Build second edge set inline to avoid a separate iteration.
		for _, depID := range newT.GetDirectDependencies() {
			if depName := secondIDMapping[depID]; depName != "" {
				secondEdges[edgeKey{source: name, dep: depName}] = struct{}{}
			}
		}

		oldT, exists := firstByName[name]
		if !exists {
			// Transpose once; share the pointer between changedByName and addedTargets.
			transposed := transposeOptimizedTarget(
				newT,
				secondIDMapping,
				secondMetadata.GetRuleTypeMapping(),
				secondMetadata.GetTagMapping(),
				secondMetadata.GetAttributeNameMapping(),
				secondMetadata.GetAttributeStringValueMapping(),
				getTargetId, getRuleTypeId, getTagId, getAttrNameId, getAttrValId,
			)
			changedByName[name] = &pb.ChangedTarget{
				ChangeType: pb.CHANGE_TYPE_NEW,
				NewTarget:  transposed,
				Distance:   getDefaultDistance(outputConfig, true),
			}
			addedTargets = append(addedTargets, transposed)
			continue
		}
		if oldT.GetHash() == newT.GetHash() {
			continue
		}
		initial := pb.CHANGE_TYPE_INDIRECT
		isSource := newT.GetRuleType() == sourceFileRuleTypeID && sourceFileRuleTypeID != -1
		if isSource {
			initial = pb.CHANGE_TYPE_DIRECT
			changedSourceFileTargets[name] = struct{}{}
		}
		newTarget := transposeOptimizedTarget(
			newT,
			secondIDMapping,
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
			ChangeType: initial,
			OldTarget:  oldTarget,
			NewTarget:  newTarget,
			Distance:   getDefaultDistance(outputConfig, false),
		}
	}

	// 4) Classify INDIRECT changes as DIRECT where appropriate.
	for name, ct := range changedByName {
		if ct.GetChangeType() == pb.CHANGE_TYPE_DIRECT || ct.GetChangeType() == pb.CHANGE_TYPE_NEW {
			continue
		}
		newT := secondByName[name]
		oldT := firstByName[name]

		if hasDepInChangedSourceFileTargets(newT.GetDirectDependencies(), secondMetadata, changedSourceFileTargets) {
			ct.ChangeType = pb.CHANGE_TYPE_DIRECT
			continue
		}
		depsChanged, err := dependenciesChanged(oldT, firstMetadata, newT, secondMetadata)
		if err != nil {
			return nil, fmt.Errorf("failed to check dependencies changed: %w", err)
		}
		if depsChanged {
			ct.ChangeType = pb.CHANGE_TYPE_DIRECT
			continue
		}
		attrsChanged, err := attributesChanged(oldT, firstMetadata, newT, secondMetadata)
		if err != nil {
			return nil, fmt.Errorf("failed to check attributes changed: %w", err)
		}
		if attrsChanged {
			ct.ChangeType = pb.CHANGE_TYPE_DIRECT
		}
	}

	// 5) Compute BFS distances if requested.
	if outputConfig.GetComputeDistances() {
		computeDistances(c.logger, changedByName, secondByName, secondMetadata, outputConfig.GetMaxDistance())
	}

	// 6) Collect changed targets.
	changed := make([]*pb.ChangedTarget, 0, len(changedByName))
	for _, ct := range changedByName {
		changed = append(changed, ct)
	}

	// 7) Single pass over first graph: collect removed targets and build first edge set.
	firstIDMapping := firstMetadata.GetTargetIdMapping()
	firstEdges := make(map[edgeKey]struct{})
	var removedTargets []*pb.OptimizedTarget
	for name, oldT := range firstByName {
		// Build first edge set inline to avoid a separate iteration.
		for _, depID := range oldT.GetDirectDependencies() {
			if depName := firstIDMapping[depID]; depName != "" {
				firstEdges[edgeKey{source: name, dep: depName}] = struct{}{}
			}
		}
		if _, exists := secondByName[name]; !exists {
			removedTargets = append(removedTargets, transposeOptimizedTarget(
				oldT,
				firstIDMapping,
				firstMetadata.GetRuleTypeMapping(),
				firstMetadata.GetTagMapping(),
				firstMetadata.GetAttributeNameMapping(),
				firstMetadata.GetAttributeStringValueMapping(),
				getTargetId, getRuleTypeId, getTagId, getAttrNameId, getAttrValId,
			))
		}
	}

	// 8) Compute new and removed edges from the sets built above.
	var newEdges []*pb.Edge
	for e := range secondEdges {
		if _, exists := firstEdges[e]; !exists {
			newEdges = append(newEdges, &pb.Edge{
				SourceId: getTargetId(e.source),
				TargetId: getTargetId(e.dep),
			})
		}
	}
	var removedEdges []*pb.Edge
	for e := range firstEdges {
		if _, exists := secondEdges[e]; !exists {
			removedEdges = append(removedEdges, &pb.Edge{
				SourceId: getTargetId(e.source),
				TargetId: getTargetId(e.dep),
			})
		}
	}

	// 10) Build canonical metadata.
	meta := &pb.Metadata{
		TargetIdMapping:             targetMapper.Invert(),
		RuleTypeMapping:             ruleTypeMapper.Invert(),
		TagMapping:                  tagMapper.Invert(),
		AttributeNameMapping:        attrNameMapper.Invert(),
		AttributeStringValueMapping: attrValMapper.Invert(),
	}

	totalDuration := time.Since(start)
	c.logger.Info("compareTargetGraphsAndEdges: Done",
		zap.Duration("total_duration", totalDuration),
	)
	scope.Timer("total_duration").Record(totalDuration)

	return []*pb.GetChangedTargetsAndEdgesResponse{
		{
			Item: &pb.GetChangedTargetsAndEdgesResponse_ChangedTargetsAndEdges{
				ChangedTargetsAndEdges: &pb.ChangedTargetsAndEdges{
					ChangedTargets: changed,
					AddedTargets:   addedTargets,
					RemovedTargets: removedTargets,
					NewEdges:       newEdges,
					RemovedEdges:   removedEdges,
				},
			},
		},
		{
			Item: &pb.GetChangedTargetsAndEdgesResponse_Metadata{
				Metadata: meta,
			},
		},
	}, nil
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

func validateGetChangedTargetsAndEdgesRequest(request *pb.GetChangedTargetsAndEdgesRequest) error {
	if request == nil {
		return errors.New("request cannot be nil")
	}
	if request.GetFirstRevision() == nil {
		return errors.New("first revision is required")
	}
	if request.GetSecondRevision() == nil {
		return errors.New("second revision is required")
	}
	firstRevision := request.GetFirstRevision()
	if firstRevision.GetRemote() == "" {
		return errors.New("first revision remote is required")
	}
	if firstRevision.GetBaseSha() == "" {
		return errors.New("first revision base_sha is required")
	}
	secondRevision := request.GetSecondRevision()
	if secondRevision.GetRemote() == "" {
		return errors.New("second revision remote is required")
	}
	if secondRevision.GetBaseSha() == "" {
		return errors.New("second revision base_sha is required")
	}
	if firstRevision.GetRemote() != secondRevision.GetRemote() {
		return errors.New("first and second revision must have the same remote")
	}
	return nil
}
