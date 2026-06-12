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
	"go.uber.org/zap"
)

// job represents a single goroutine of getting a target graph
type job struct {
	graphStreamChunks []*pb.GetTargetGraphResponse
	err               error
	cancelled         bool
	completed         bool
	ctx               context.Context
	cancel            context.CancelFunc
}

// GetChangedTargets returns the changed targets between two revisions. If the
// client disconnects, the stream's context is cancelled and the function
// returns with context.Canceled.
func (c *controller) GetChangedTargets(request *pb.GetChangedTargetsRequest, stream pb.TangoServiceGetChangedTargetsYARPCServer) (retErr error) {
	scope := c.scope.SubScope("get_changed_targets")
	scope.Counter("calls").Inc(1)
	defer func() {
		if retErr != nil {
			scope.Counter("failure").Inc(1)
			emitFailureMetric(scope, retErr)
		} else {
			scope.Counter("success").Inc(1)
		}
	}()
	if err := validateGetChangedTargetsRequest(request); err != nil {
		c.logger.Error("GetChangedTargets: Invalid request", zap.Error(err))
		return common.WithReason(failureReasonValidation, common.ErrorTypeUser, err)
	}
	scope = scope.Tagged(map[string]string{"repo": common.ToShortRemote(request.GetFirstRevision().GetRemote())})
	ctx := stream.Context()
	start := time.Now()
	logger := c.logger.With(
		zap.Any("first_revision", request.GetFirstRevision()),
		zap.Any("second_revision", request.GetSecondRevision()),
	)

	logger.Info("GetChangedTargets: Processing request")

	// Default max_distance to -1 (no filtering) when the client omits OutputConfig
	// entirely. When OutputConfig is supplied, take max_distance at face value —
	// see proto/tango.proto OutputConfig.max_distance for the wire-default caveat.
	maxDist := int32(-1)
	if request.GetOutputConfig() != nil {
		maxDist = request.GetOutputConfig().GetMaxDistance()
	}

	// Try to serve from cache first using the stored treehashes for both revisions.
	// readTreehash returns "" on any miss/error so we silently skip the cache when
	// either treehash is not yet available.
	if !request.GetBypassCache() {
		cacheStart := time.Now()
		treehash1 := readTreehash(ctx, c.storage, request.GetFirstRevision())
		treehash2 := readTreehash(ctx, c.storage, request.GetSecondRevision())
		if treehash1 != "" && treehash2 != "" {
			cacheKey := common.GetComparedTargetsCachePath(request.GetFirstRevision().GetRemote(), treehash1, treehash2, request.GetRequestOptions())
			cachedReader, cacheErr := storage.NewChangedTargetsReader(ctx, c.storage, cacheKey)
			if cacheErr != nil && !storage.IsNotFound(cacheErr) {
				logger.Warn("GetChangedTargets: Failed to read from cache, proceeding to compute", zap.Error(cacheErr))
			} else if cachedReader != nil {
				// Buffer all responses before sending any. A concurrent goroutine write may have
				// left a partial blob in storage; buffering lets us detect corruption and fall
				// through to recompute before we've sent anything to the client.
				var cached []*pb.GetChangedTargetsResponse
				var readErr error
				for {
					var resp *pb.GetChangedTargetsResponse
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
					// Blob is corrupt (likely an incomplete write). Log and fall through to recompute.
					logger.Warn("GetChangedTargets: Cached result is incomplete, recomputing", zap.Error(readErr))
				} else {
					cacheReadDuration := time.Since(cacheStart)
					logger.Info("GetChangedTargets: Cache hit, streaming from storage",
						zap.Duration("cache_read_duration", cacheReadDuration),
					)
					scope.Counter("cache_hit").Inc(1)
					scope.Timer("cache_read_duration").Record(cacheReadDuration)
					if sendErr := sendTrimmedChangedTargets(stream, cached, maxDist, request.GetOutputConfig()); sendErr != nil {
						logger.Error("GetChangedTargets: Failed to send cached response", zap.Error(sendErr))
						return common.WithReason(failureReasonSend, common.ErrorTypeInfra, fmt.Errorf("failed to send cached response: %w", sendErr))
					}
					totalDuration := time.Since(start)
					logger.Info("GetChangedTargets: Successfully streamed from cache",
						zap.Duration("total_duration", totalDuration),
					)
					scope.Timer("total_duration").Record(totalDuration)
					scope.Histogram("total_duration.histogram", c.totalDurationBuckets).RecordDuration(totalDuration)
					return nil
				}
			}
		}
	}

	jobs := make([]*job, 2)

	for i := 0; i < 2; i++ {
		// create independent contexts for each job; if one of the jobs fails, the other one should be cancelled to save resources and improve latency
		ctxNew, cancelNew := context.WithCancel(ctx)
		defer cancelNew()
		jobs[i] = &job{ctx: ctxNew, cancel: cancelNew}
	}

	// Start jobs for both revisions. Success or failure, the result will report to the results channel.
	type graphResult struct {
		// order is 0 or 1, 0 is the base (first) revision, 1 is the target (second) revision
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
			graphReader, err := c.getGraph(jobs[idx].ctx, revision, request.GetOutputConfig(), request.GetRequestOptions(), request.GetBypassCache())
			if err != nil || graphReader == nil {
				results <- graphResult{order: idx, err: err}
				return
			}
			defer graphReader.Close()

			// Read all chunks from the stream
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

	// Wait for both results to complete, either successfully or with an error.
	for range jobs {
		select {
		case res := <-results:
			jobs[res.order].graphStreamChunks = res.chunks
			jobs[res.order].completed = true
			jobs[res.order].err = res.err
			if res.chunks == nil && res.err == nil {
				jobs[res.order].err = errors.New("no chunks returned")
			}

			// one of the computations failed, if the other one has not
			// completed yet, cancel it and wait for the result to come in,
			// which would be a context cancelled result then
			if jobs[res.order].err != nil {
				other := (res.order + 1) % 2
				if !jobs[other].completed {
					jobs[other].cancel()
					// explicitly mark that this job is cancelled, so we can
					// ignore its error later
					jobs[other].cancelled = true
				}
			}
		}
	}

	graphFetchDuration := time.Since(graphFetchStart)
	logger.Info("GetChangedTargets: Both graphs fetched",
		zap.Duration("graph_fetch_duration", graphFetchDuration),
	)
	scope.Timer("graph_fetch_duration").Record(graphFetchDuration)

	if ctx.Err() != nil {
		// If the context was cancelled by the upstream, just return the original error without additional augmentation
		return common.WithReason(failureReasonCancelled, common.ErrorTypeUser, ctx.Err())
	}

	// Process errors, only aggregating the ones that are original ones and not a result of the other job being cancelled
	var err error
	for i, job := range jobs {
		if job.err != nil {
			if job.cancelled {
				// this only happens as a result of the other job failing, so we can ignore the error
				continue
			}
			err = errors.Join(err, fmt.Errorf("failed to get target graph #%d: %w", i+1, job.err))
		}
	}

	if err != nil {
		return err
	}
	firstGraph := jobs[0].graphStreamChunks
	secondGraph := jobs[1].graphStreamChunks
	// Drop job references so the GC can reclaim them once the comparison is done.
	jobs[0].graphStreamChunks = nil
	jobs[1].graphStreamChunks = nil

	compareStart := time.Now()
	changedTargetsResponses, err := c.compareTargetGraphs(ctx, logger, firstGraph, secondGraph, maxDist)
	// Allow GC of raw graph data while the caching goroutine runs.
	firstGraph = nil
	secondGraph = nil
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
	// changedTargetsResponses, so concurrent access is safe.
	go func() {
		cacheCtx := context.Background()
		treehash1 := readTreehash(cacheCtx, c.storage, request.GetFirstRevision())
		treehash2 := readTreehash(cacheCtx, c.storage, request.GetSecondRevision())
		if treehash1 != "" && treehash2 != "" {
			cacheKey := common.GetComparedTargetsCachePath(request.GetFirstRevision().GetRemote(), treehash1, treehash2, request.GetRequestOptions())
			if writeErr := storage.WriteChangedTargetsStream(cacheCtx, c.storage, cacheKey, changedTargetsResponses); writeErr != nil {
				logger.Warn("GetChangedTargets: Failed to cache result", zap.Error(writeErr))
			}
		}
	}()

	sendStart := time.Now()
	if err := sendTrimmedChangedTargets(stream, changedTargetsResponses, maxDist, request.GetOutputConfig()); err != nil {
		logger.Error("GetChangedTargets: Failed to send response", zap.Error(err))
		return common.WithReason(failureReasonSend, common.ErrorTypeInfra, fmt.Errorf("failed to send response: %w", err))
	}
	sendDuration := time.Since(sendStart)
	scope.Timer("send_duration").Record(sendDuration)

	totalDuration := time.Since(start)
	logger.Info("GetChangedTargets: Successfully processed request",
		zap.Duration("send_duration", sendDuration),
		zap.Duration("total_duration", totalDuration),
	)
	scope.Timer("total_duration").Record(totalDuration)
	scope.Histogram("total_duration.histogram", c.totalDurationBuckets).RecordDuration(totalDuration)
	return nil
}

// compareTargetGraphs diffs two target graph streams and produces a chunked
// GetChangedTargetsResponse stream. Targets are classified as NEW (only in
// second), DELETED (only in first), or CHANGED (present in both, differs).
// Distances are always computed: a target is a distance-0 seed when it is
// NEW, DELETED, a source file with a changed hash, or a rule whose own
// configuration (attributes or direct deps) changed. All other CHANGED
// targets get their distance from BFS over the reverse-dep graph.
// Output IDs are re-mapped into a canonical per-call namespace so the
// response metadata only carries the names actually referenced.
func (c *controller) compareTargetGraphs(ctx context.Context, logger *zap.Logger, firstGraph, secondGraph []*pb.GetTargetGraphResponse, maxDist int32) ([]*pb.GetChangedTargetsResponse, error) {
	start := time.Now()
	scope := c.scope.SubScope("compare_target_graphs")
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
	scope.Timer("index_duration").Record(indexDuration)

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	sourceFileRuleTypeID := detectSourceFileID(secondMetadata)

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	changedByName := make(map[string]*pb.ChangedTarget)
	// seeds are targets whose own state changed: NEW, DELETED, a source file
	// whose hash changed, or a rule whose attributes or direct-deps changed.
	// BFS over reverse-deps assigns distance 0 to seeds and distance >=1 to
	// downstream consumers.
	seeds := make(map[string]struct{})

	// 3) Create canonical mappers for IDs (targets, rule types, tags, attributes)
	targetMapper := common.NewNameIDMapper()
	ruleTypeMapper := common.NewNameIDMapper()
	tagMapper := common.NewNameIDMapper()
	attrNameMapper := common.NewNameIDMapper()
	attrValMapper := common.NewNameIDMapper()
	// These functions are used to transpose the target into the canonical ID space.
	// When called, we attempt to find the ID for the name in the metadata and return the ID.
	getTargetId := func(name string) int32 { return targetMapper.ID(name) }
	getRuleTypeId := func(name string) int32 { return ruleTypeMapper.ID(name) }
	getTagId := func(name string) int32 { return tagMapper.ID(name) }
	getAttrNameId := func(name string) int32 { return attrNameMapper.ID(name) }
	getAttrValId := func(name string) int32 { return attrValMapper.ID(name) }

	// Pass 1: walk second revision. Targets not in first revision are NEW (seeds).
	// Targets in both with differing hashes are CHANGED; source-file CHANGED
	// targets are also seeds (rule consumers will pick up distance >= 1 via BFS).
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
			// same hash -> unchanged
			continue
		}
		// Source files with a hash change are seeds; rules will be evaluated
		// for own-config changes in pass 2 below.
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
	scope.Timer("diff_scan_duration").Record(diffScanDuration)

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Pass 2: decide which CHANGED rule targets are seeds (distance 0).
	//
	// We trust the hasher: a CHANGED entry means the rule's hash differs.
	// The only reason a hash change should land at distance >= 1 (rather than
	// 0) is when it is fully explained by a *direct dep* having changed —
	// i.e. the change is purely transitive. In every other case the rule is
	// a seed.
	//
	// Concretely, attribute / dep-list inspection is only needed to promote
	// a rule that BFS would otherwise put at distance 1 (because a dep
	// changed) down to distance 0 (because the rule's own configuration
	// changed too). If no dep changed, the hash change has no upstream
	// explanation and the rule is a seed regardless of what inspection says.
	classifyStart := time.Now()
	for name, ct := range changedByName {
		if _, isSeed := seeds[name]; isSeed {
			// Already a seed (NEW or changed source file).
			continue
		}
		if ct.GetChangeType() != pb.CHANGE_TYPE_CHANGED {
			continue
		}
		newT := secondByName[name]
		oldT := firstByName[name]

		// Single pass over newT.deps decides two things at once: whether any
		// direct dep itself changed (would otherwise transitively explain the
		// hash diff at distance >= 1), and whether the rule's own dep-name set
		// changed (its own configuration changed → seed).
		anyChanged, depsChanged := changedDepStatus(oldT, firstMetadata, newT, secondMetadata, changedByName)
		if !anyChanged {
			// No direct dep changed: the hash diff has no upstream explanation,
			// trust the hasher and seed.
			seeds[name] = struct{}{}
			continue
		}
		if depsChanged {
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
	scope.Timer("classify_duration").Record(classifyDuration)

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Pass 3: emit DELETED entries for targets present only in the first revision.
	// Deletions are seeds (distance 0) but have no entries in secondByName /
	// reverseDeps, so BFS naturally propagates nothing from them.
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
	scope.Timer("distances_duration").Record(distancesDuration)

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
		targetMapper.Invert(),
		ruleTypeMapper.Invert(),
		tagMapper.Invert(),
		attrNameMapper.Invert(),
		attrValMapper.Invert(),
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
	scope.Timer("total_duration").Record(totalDuration)
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
	// check the id in the rule type mapping for "source file"
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

	// If lengths differ, setDiffered is trivially true — no need to allocate
	// a name set for membership checks.
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

// attributesChanged checks if the attributes changed between old and new targets.
func attributesChanged(oldTarget *pb.OptimizedTarget, oldMeta *pb.Metadata, newTarget *pb.OptimizedTarget, newMeta *pb.Metadata) (bool, error) {
	if oldMeta == nil || newMeta == nil {
		return false, nil
	}
	// validate target names are equivalent.
	if err := validateTargetNames(oldTarget, newTarget, oldMeta, newMeta); err != nil {
		return false, err
	}

	oldAttrIDs := oldTarget.GetAttributes()
	newAttrIDs := newTarget.GetAttributes()

	// Early exit: if lengths differ, attributes changed
	if len(oldAttrIDs) != len(newAttrIDs) {
		return true, nil
	}

	// Early exit: if both are empty, no change
	if len(oldAttrIDs) == 0 {
		return false, nil
	}

	// Cache metadata mappings to avoid repeated map lookups
	oldAttrNameMapping := oldMeta.GetAttributeNameMapping()
	oldAttrValMapping := oldMeta.GetAttributeStringValueMapping()
	newAttrNameMapping := newMeta.GetAttributeNameMapping()
	newAttrValMapping := newMeta.GetAttributeStringValueMapping()

	// Build map of new attributes (only one map needed)
	newAttrMap := make(map[string]string, len(newAttrIDs))
	for attrNameID, attrValID := range newAttrIDs {
		if attrName := newAttrNameMapping[attrNameID]; attrName != "" {
			newAttrMap[attrName] = newAttrValMapping[attrValID]
		}
	}

	// Check if all old attributes match
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

// validateTargetNames checks if the target names are the same between old and new targets, and exists in both metadata maps.
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

// sendTrimmedChangedTargets streams responses to the client, filtering changed targets to those
// within maxDist from any distance-0 seed when maxDist >= 0, stripping per-target
// hash/tags/attributes per outputConfig's include_* flags, and pruning metadata mappings
// whose IDs are no longer referenced. Filtering and sending are combined into a single pass
// to avoid an intermediate allocation.
func sendTrimmedChangedTargets(stream pb.TangoServiceGetChangedTargetsYARPCServer, responses []*pb.GetChangedTargetsResponse, maxDist int32, outputConfig *pb.OutputConfig) error {
	stripFields := optimizedTargetNeedsStripping(outputConfig)
	pruneMeta := metadataNeedsPruning(outputConfig)
	for _, resp := range responses {
		toSend := resp
		switch item := resp.GetItem().(type) {
		case *pb.GetChangedTargetsResponse_ChangedTargets:
			if maxDist >= 0 || stripFields {
				kept := item.ChangedTargets.GetChangedTargets()
				if maxDist >= 0 {
					kept = filterChangedTargetsByDistance(kept, maxDist)
				}
				kept = applyChangedTargetsOutputConfig(kept, outputConfig)
				toSend = &pb.GetChangedTargetsResponse{
					Item: &pb.GetChangedTargetsResponse_ChangedTargets{
						ChangedTargets: &pb.ChangedTargets{ChangedTargets: kept},
					},
				}
			}
		case *pb.GetChangedTargetsResponse_Metadata:
			if pruneMeta {
				toSend = &pb.GetChangedTargetsResponse{
					Item: &pb.GetChangedTargetsResponse_Metadata{
						Metadata: applyMetadataOutputConfig(item.Metadata, outputConfig),
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

// computeDistances assigns each CHANGED target its BFS distance from the
// nearest distance-0 seed in the reverse-dependency graph. Seeds are passed in
// pre-classified and start at distance 0; everything else starts at -1 and
// gets overwritten if reachable. Targets beyond `maxDistance` (when >= 0) are
// never enqueued, so they keep their initial distance of -1 (out-of-range).
func computeDistances(ctx context.Context, changedByName map[string]*pb.ChangedTarget, targetsByName map[string]*pb.OptimizedTarget, meta *pb.Metadata, seeds map[string]struct{}, maxDistance int32) error {
	if meta == nil {
		return nil
	}

	targetIDMapping := meta.GetTargetIdMapping()

	// Build reverse dependency graph: if B depends on A, then A -> B.
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

	// Initialize all distances. Seeds at 0 and enqueued; everything else at -1.
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

	// BFS from seeds through reverseDeps. Shortest distance wins.
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
			// BFS guarantees shortest distance, so skip if already visited.
			if _, seen := visited[revDep]; seen {
				continue
			}
			nextDist := currentDist + 1
			// Prune: if a maxDistance is set and the next distance exceeds it, skip.
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

// validateGetChangedTargetsRequest enforces the minimal invariants the
// comparison pipeline relies on: both revisions present, both populated
// with a remote and base SHA, and both pointing at the same remote.
// OutputConfig is optional; when omitted, max_distance defaults to -1
// (no filtering). See proto/tango.proto OutputConfig.max_distance for
// the wire-default caveat when OutputConfig is supplied without
// max_distance set.
func validateGetChangedTargetsRequest(request *pb.GetChangedTargetsRequest) error {
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
	// Validate that both revisions have the same remote
	if firstRevision.GetRemote() != secondRevision.GetRemote() {
		return errors.New("first and second revision must have the same remote")
	}
	return nil
}

// readTreehash fetches the treehash stored at GetTreehashCachePath for the given build description.
// Returns an empty string on any error or cache miss so callers can treat it as an optional optimistic lookup.
func readTreehash(ctx context.Context, st storage.Storage, buildDescription *pb.BuildDescription) string {
	resp, err := st.Get(ctx, storage.DownloadRequest{Key: common.GetTreehashCachePath(buildDescription)})
	if err != nil || resp == nil || resp.ReadCloser == nil {
		return ""
	}
	defer resp.ReadCloser.Close()
	b, err := io.ReadAll(resp.ReadCloser)
	if err != nil {
		return ""
	}
	return string(b)
}
