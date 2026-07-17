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

	"github.com/uber-go/tally"
	"github.com/uber/tango/core/cachekey"
	"github.com/uber/tango/core/common"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/mapper"
	"github.com/uber/tango/internal/mapper/idmapper"
	"github.com/uber/tango/internal/targetdiff"
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
	logger := c.logger.WithLazy(
		zap.Any("first_revision", request.GetFirstRevision()),
		zap.Any("second_revision", request.GetSecondRevision()),
	)
	defer func() {
		if retErr != nil {
			logger.Error("GetChangedTargets failed", zap.Error(retErr))
			scope.Counter("failure").Inc(1)
			emitFailureMetric(scope, retErr)
		} else {
			scope.Counter("success").Inc(1)
		}
	}()
	if err := validateGetChangedTargetsRequest(request); err != nil {
		return common.WithReason(common.FailureReasonValidation, common.ErrorTypeUser, err)
	}
	scope = scope.Tagged(map[string]string{"repo": common.ToShortRemote(request.GetFirstRevision().GetRemote())})
	ctx, cancelLink := c.linkRequestCtx(stream.Context())
	defer cancelLink()
	start := time.Now()

	logger.Info("GetChangedTargets: Processing request")

	// Default max_distance to -1 (no filtering) when the client omits OutputConfig
	// entirely. When OutputConfig is supplied, take max_distance at face value —
	// see proto/tango.proto OutputConfig.max_distance for the wire-default caveat.
	maxDist := int32(-1)
	if request.GetOutputConfig() != nil {
		maxDist = request.GetOutputConfig().GetMaxDistance()
	}

	// Fast path: stream a previously computed result straight from cache.
	if !request.GetBypassCache() {
		served, err := c.serveChangedTargetsFromCache(ctx, scope, logger, request, stream, maxDist, start)
		if err != nil {
			return err
		}
		if served {
			return nil
		}
	}

	// Fetch both revisions' target graphs concurrently.
	firstGraph, secondGraph, err := c.fetchTargetGraphs(ctx, scope, logger, request)
	if err != nil {
		return err
	}

	changedTargetsResponses, err := c.compareTargetGraphs(ctx, scope, logger, firstGraph, secondGraph, maxDist)
	// Allow GC of raw graph data while the caching goroutine runs.
	firstGraph = nil
	secondGraph = nil
	if err != nil {
		if ctx.Err() != nil {
			return common.WithReason(common.FailureReasonCancelled, common.ErrorTypeUser, ctx.Err())
		}
		return common.WithReason(failureReasonCompare, common.ErrorTypeInfra, fmt.Errorf("failed to compare target graphs: %w", err))
	}

	// Cache the computed result concurrently so it doesn't block the stream send.
	c.cacheComparedTargets(logger, request, changedTargetsResponses)

	sendStart := time.Now()
	if err := sendTrimmedChangedTargets(stream, changedTargetsResponses, maxDist, request.GetOutputConfig()); err != nil {
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

// serveChangedTargetsFromCache attempts to stream a previously computed
// compared-targets result straight from storage. It returns:
//   - (true, nil)  when a cached result was found and fully sent to the client;
//   - (false, nil) on a cache miss or a corrupt blob — the caller should recompute;
//   - (false, err) on an infra failure or a client disconnect that aborts the request.
//
// readTreehash returns ("", nil) on a cache miss (skip cache, recompute) but any
// real storage error surfaces here so an infra failure that disables the cache
// (e.g. a missing-deadline "missing TTL" reject) becomes a visible request failure
// rather than silent degradation.
func (c *controller) serveChangedTargetsFromCache(ctx context.Context, scope tally.Scope, logger *zap.Logger, request *pb.GetChangedTargetsRequest, stream pb.TangoServiceGetChangedTargetsYARPCServer, maxDist int32, start time.Time) (bool, error) {
	cacheStart := time.Now()
	treehash1, treehash2, err := readTreehashParallel(ctx, c.storage, request.GetFirstRevision(), request.GetSecondRevision())
	if err != nil {
		return false, common.WithReason(failureReasonTreehashRead, common.ErrorTypeInfra, err)
	}
	if treehash1 == "" || treehash2 == "" {
		return false, nil
	}

	cacheKey := cachekey.GetComparedTargetsCachePath(request.GetFirstRevision().GetRemote(), treehash1, treehash2, request.GetRequestOptions().GetExtraExcludeFilesRegex())
	cachedReader, cacheErr := storage.NewChangedTargetsReader(ctx, c.storage, cacheKey)
	if cacheErr != nil && !storage.IsNotFound(cacheErr) {
		logger.Warn("GetChangedTargets: Failed to read from cache, proceeding to compute", zap.Error(cacheErr))
		return false, nil
	}
	if cachedReader == nil {
		return false, nil
	}

	// Buffer all responses before sending any. A concurrent goroutine write may have
	// left a partial blob in storage; buffering lets us detect corruption and fall
	// through to recompute before we've sent anything to the client.
	var cached []*pb.GetChangedTargetsResponse
	var readErr error
	for {
		if err := ctx.Err(); err != nil {
			cachedReader.Close()
			// Client gave up while we were draining the cache. Surface as a user-cancelled error.
			return false, common.WithReason(common.FailureReasonCancelled, common.ErrorTypeUser, err)
		}
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
		return false, nil
	}

	cacheReadDuration := time.Since(cacheStart)
	logger.Info("GetChangedTargets: Cache hit, streaming from storage",
		zap.Duration("cache_read_duration", cacheReadDuration),
	)
	scope.Counter("changed_targets_cache_hit").Inc(1)
	scope.Timer("cache_read_duration").Record(cacheReadDuration)
	if sendErr := sendTrimmedChangedTargets(stream, cached, maxDist, request.GetOutputConfig()); sendErr != nil {
		return false, common.WithReason(failureReasonSend, common.ErrorTypeInfra, fmt.Errorf("failed to send cached response: %w", sendErr))
	}
	totalDuration := time.Since(start)
	logger.Info("GetChangedTargets: Successfully streamed from cache",
		zap.Duration("total_duration", totalDuration),
	)
	scope.Timer("total_duration").Record(totalDuration)
	scope.Histogram("total_duration.histogram", c.totalDurationBuckets).RecordDuration(totalDuration)
	return true, nil
}

// fetchTargetGraphs computes both revisions' target graphs concurrently. Each
// fetch runs under its own cancellable context so that, when one fails, the
// sibling is cancelled to avoid wasting work on a result that will be discarded.
// Errors caused solely by that induced cancellation are dropped; only the
// original failure is returned. A client disconnect surfaces as a user-cancelled
// error.
func (c *controller) fetchTargetGraphs(ctx context.Context, scope tally.Scope, logger *zap.Logger, request *pb.GetChangedTargetsRequest) ([]*pb.GetTargetGraphResponse, []*pb.GetTargetGraphResponse, error) {
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
			entityBuild, err := mapper.ProtoToBuildDescription(revision)
			if err != nil {
				results <- graphResult{order: idx, err: fmt.Errorf("convert build description: %w", err)}
				return
			}
			entityReq := entity.GetTargetGraphRequest{
				Build:             entityBuild,
				ExcludeFilesRegex: request.GetRequestOptions().GetExtraExcludeFilesRegex(),
				BypassCache:       request.GetBypassCache(),
			}
			graphReader, err := c.getGraph(jobs[idx].ctx, entityReq)
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
		res := <-results
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

	graphFetchDuration := time.Since(graphFetchStart)
	logger.Info("GetChangedTargets: Both graphs fetched",
		zap.Duration("graph_fetch_duration", graphFetchDuration),
	)
	scope.Timer("graph_fetch_duration").Record(graphFetchDuration)

	if ctx.Err() != nil {
		// If the context was cancelled by the upstream, just return the original error without additional augmentation
		return nil, nil, common.WithReason(common.FailureReasonCancelled, common.ErrorTypeUser, ctx.Err())
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
		return nil, nil, err
	}

	firstGraph := jobs[0].graphStreamChunks
	secondGraph := jobs[1].graphStreamChunks
	// Drop job references so the GC can reclaim them once the comparison is done.
	jobs[0].graphStreamChunks = nil
	jobs[1].graphStreamChunks = nil
	return firstGraph, secondGraph, nil
}

// cacheComparedTargets writes the computed compared-targets result to storage in
// a fire-and-forget goroutine so it does not block the stream send. The responses
// is only read (never mutated) by the goroutine and the foreground send, so
// concurrent access is safe; the caller must not mutate it. This is best effort.
func (c *controller) cacheComparedTargets(logger *zap.Logger, request *pb.GetChangedTargetsRequest, responses []*pb.GetChangedTargetsResponse) {
	go func() {
		// Use c.appCtx directly: the cache write is fire-and-forget and must
		// outlive the request (so a client disconnect doesn't abort it) but
		// must NOT outlive the server (so it doesn't leak past shutdown).
		// c.appCtx fits both: it's never cancelled by client disconnect and
		// is cancelled on shutdown. Per-operation deadlines are the storage
		// backend's responsibility — the controller is backend-agnostic and
		// must not encode any one implementation's I/O budget.
		treehash1, treehash2, err := readTreehashParallel(c.appCtx, c.storage, request.GetFirstRevision(), request.GetSecondRevision())
		if err != nil {
			// Goroutine outlives the handler so we can't return; log loudly and
			// abandon the cache write. Surfacing infra failures matters more than
			// a missed cache opportunity.
			logger.Warn("GetChangedTargets: skipping cache write, failed to read revision treehash", zap.Error(err))
			return
		}
		if treehash1 != "" && treehash2 != "" {
			cacheKey := cachekey.GetComparedTargetsCachePath(request.GetFirstRevision().GetRemote(), treehash1, treehash2, request.GetRequestOptions().GetExtraExcludeFilesRegex())
			if writeErr := storage.WriteChangedTargetsStream(c.appCtx, c.storage, cacheKey, responses); writeErr != nil {
				logger.Warn("GetChangedTargets: Failed to cache result", zap.Error(writeErr))
			}
		} else {
			logger.Warn("GetChangedTargets: skipping compared-targets cache write, missing treehash",
				zap.Bool("treehash1_empty", treehash1 == ""),
				zap.Bool("treehash2_empty", treehash2 == ""))
		}
	}()
}

// compareTargetGraphs diffs two target graph streams and produces a chunked
// GetChangedTargetsResponse stream. Each stream is decoded into a semantic
// targetdiff.Graph (int32 IDs resolved to names via that stream's metadata),
// the two graphs are compared by internal/targetdiff, and the resulting changes
// are re-mapped into a canonical per-call ID namespace so the response metadata
// only carries the names actually referenced. See internal/targetdiff for the
// classification and distance rules.
func (c *controller) compareTargetGraphs(ctx context.Context, scope tally.Scope, logger *zap.Logger, firstGraph, secondGraph []*pb.GetTargetGraphResponse, maxDist int32) ([]*pb.GetChangedTargetsResponse, error) {
	start := time.Now()
	compareScope := scope.SubScope("compare_target_graphs")
	logger.Info("compareTargetGraphs: Computing differences between target graphs")

	// 1) Decode each stream into a semantic graph keyed by canonical target name.
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
	before, err := toDiffGraph(ctx, firstTargetsByID, firstMetadata)
	if err != nil {
		return nil, err
	}
	// Metadata and ID map are fully consumed by the name-resolved graph; drop them.
	firstTargetsByID = nil
	firstMetadata = nil
	after, err := toDiffGraph(ctx, secondTargetsByID, secondMetadata)
	if err != nil {
		return nil, err
	}
	secondTargetsByID = nil
	secondMetadata = nil
	indexDuration := time.Since(indexStart)
	compareScope.Timer("index_duration").Record(indexDuration)

	// 2) Compare the two semantic graphs.
	computeStart := time.Now()
	result, err := targetdiff.Compare(ctx, targetdiff.Request{
		Before:      before,
		After:       after,
		MaxDistance: maxDist,
	})
	if err != nil {
		return nil, err
	}
	// Release the input graphs; only result is needed from here on.
	before = nil
	after = nil
	compareScope.Timer("compute_duration").Record(time.Since(computeStart))

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// 3) Re-map each change into a canonical per-call ID namespace. The mappers
	// only assign IDs to names they actually see, so the emitted metadata is
	// pruned to what the changed targets reference.
	mappers := newCanonicalMappers()
	changed := make([]*pb.ChangedTarget, 0, len(result.ChangedTargets))
	for _, ct := range result.ChangedTargets {
		changed = append(changed, &pb.ChangedTarget{
			ChangeType: toChangeType(ct.ChangeType),
			OldTarget:  mappers.transpose(ct.Before),
			NewTarget:  mappers.transpose(ct.After),
			Distance:   ct.Distance,
		})
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
		mappers.target.Invert(),
		mappers.ruleType.Invert(),
		mappers.tag.Invert(),
		mappers.attrName.Invert(),
		mappers.attrVal.Invert(),
		c.metadataMapChunkSize,
	) {
		results = append(results, &pb.GetChangedTargetsResponse{
			Item: &pb.GetChangedTargetsResponse_Metadata{
				Metadata: meta,
			},
		})
	}
	totalDuration := time.Since(start)
	compareScope.Timer("total_duration").Record(totalDuration)
	// This helper owns its own timing/log on the request scope (mirroring
	// fetchTargetGraphs) rather than leaving it to the caller.
	logger.Info("GetChangedTargets: Target graphs compared",
		zap.Duration("compare_duration", totalDuration),
	)
	scope.Timer("compare_duration").Record(totalDuration)
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

// toDiffGraph resolves a stream's int32 IDs into a semantic targetdiff.Graph
// keyed by canonical target name. Targets with no name mapping are skipped;
// dependency, tag, and attribute IDs that don't resolve are dropped.
func toDiffGraph(ctx context.Context, targetsByID map[int32]*pb.OptimizedTarget, meta *pb.Metadata) (targetdiff.Graph, error) {
	targetIDMap := meta.GetTargetIdMapping()
	ruleTypeMap := meta.GetRuleTypeMapping()
	tagMap := meta.GetTagMapping()
	attrNameMap := meta.GetAttributeNameMapping()
	attrValMap := meta.GetAttributeStringValueMapping()

	graph := make(targetdiff.Graph, len(targetsByID))
	i := 0
	for id, t := range targetsByID {
		if i%cancelCheckInterval == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		i++
		name := targetIDMap[id]
		if name == "" {
			continue
		}
		target := &targetdiff.Target{
			Name:     name,
			Hash:     t.GetHash(),
			RuleType: ruleTypeMap[t.GetRuleType()],
			Root:     t.GetRoot(),
			External: t.GetExternal(),
		}
		if deps := t.GetDirectDependencies(); len(deps) > 0 {
			target.Dependencies = make([]string, 0, len(deps))
			for _, depID := range deps {
				if depName := targetIDMap[depID]; depName != "" {
					target.Dependencies = append(target.Dependencies, depName)
				}
			}
		}
		if tags := t.GetTags(); len(tags) > 0 {
			target.Tags = make([]string, 0, len(tags))
			for _, tagID := range tags {
				if tagName := tagMap[tagID]; tagName != "" {
					target.Tags = append(target.Tags, tagName)
				}
			}
		}
		if attrs := t.GetAttributes(); len(attrs) > 0 {
			target.Attributes = make(map[string]string, len(attrs))
			for nameID, valID := range attrs {
				if attrName := attrNameMap[nameID]; attrName != "" {
					target.Attributes[attrName] = attrValMap[valID]
				}
			}
		}
		graph[name] = target
	}
	return graph, nil
}

// canonicalMappers holds the per-call name->ID mappers that unify both
// revisions into a single canonical ID namespace. Because targets are compared
// by name, transposing them back into a wire response only needs these shared
// name->ID mappers — identical names map to identical IDs regardless of revision.
type canonicalMappers struct {
	target   *idmapper.Mapper
	ruleType *idmapper.Mapper
	tag      *idmapper.Mapper
	attrName *idmapper.Mapper
	attrVal  *idmapper.Mapper
}

// newCanonicalMappers creates an empty set of canonical mappers.
func newCanonicalMappers() *canonicalMappers {
	return &canonicalMappers{
		target:   idmapper.NewMapper(),
		ruleType: idmapper.NewMapper(),
		tag:      idmapper.NewMapper(),
		attrName: idmapper.NewMapper(),
		attrVal:  idmapper.NewMapper(),
	}
}

// transpose converts a semantic targetdiff.Target into a wire OptimizedTarget,
// assigning canonical IDs to every name it references. Returns nil for a nil src.
func (m *canonicalMappers) transpose(src *targetdiff.Target) *pb.OptimizedTarget {
	if src == nil {
		return nil
	}
	dst := &pb.OptimizedTarget{
		Id:       m.target.ID(src.Name),
		Hash:     src.Hash,
		Root:     src.Root,
		External: src.External,
	}
	if len(src.Dependencies) > 0 {
		out := make([]int32, 0, len(src.Dependencies))
		for _, dep := range src.Dependencies {
			out = append(out, m.target.ID(dep))
		}
		dst.DirectDependencies = out
	}
	if src.RuleType != "" {
		dst.RuleType = m.ruleType.ID(src.RuleType)
	}
	if len(src.Tags) > 0 {
		out := make([]int32, 0, len(src.Tags))
		for _, tag := range src.Tags {
			out = append(out, m.tag.ID(tag))
		}
		dst.Tags = out
	}
	if len(src.Attributes) > 0 {
		out := make(map[int32]int32, len(src.Attributes))
		for name, val := range src.Attributes {
			out[m.attrName.ID(name)] = m.attrVal.ID(val)
		}
		dst.Attributes = out
	}
	return dst
}

// toChangeType maps a targetdiff.ChangeType to its wire equivalent.
func toChangeType(ct targetdiff.ChangeType) pb.ChangeType {
	switch ct {
	case targetdiff.ChangeTypeNew:
		return pb.CHANGE_TYPE_NEW
	case targetdiff.ChangeTypeDeleted:
		return pb.CHANGE_TYPE_DELETED
	case targetdiff.ChangeTypeChanged:
		return pb.CHANGE_TYPE_CHANGED
	default:
		return pb.CHANGE_TYPE_INVALID
	}
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

// validateGetChangedTargetsRequest enforces the minimal invariants the
// comparison pipeline relies on: both revisions present, both populated
// with a remote and base SHA, and both pointing at the same remote.
// OutputConfig is optional; when omitted, max_distance defaults to -1
// (no filtering). See proto/tango.proto OutputConfig.max_distance for
// the wire-default caveat when OutputConfig is supplied without
// max_distance set.
//
// TODO: remove once GetChangedTargets consumes entity.BuildDescription via
// internal/mapper, which already validates required fields on ProtoTo*
// conversion (see https://github.com/uber/tango/pull/189).
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

// readTreehashParallel fetches the treehashes for two build descriptions concurrently.
// Each treehash is read via readTreehash, so a cache miss yields "" (with a nil error)
// while any real storage/read failure is returned. The two reads run under a shared
// cancellable context: as soon as one read fails, the sibling is cancelled so it stops
// wasting work on a result that will be discarded anyway. The cancelled sibling's error
// is dropped — only the original failure is returned, so a self-inflicted
// context.Canceled never masks the real reason the lookup failed.
func readTreehashParallel(ctx context.Context, st storage.Storage, first, second *pb.BuildDescription) (string, string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		idx  int
		hash string
		err  error
	}
	descs := [2]*pb.BuildDescription{first, second}
	results := make(chan result, len(descs))
	for i, desc := range descs {
		go func(idx int, d *pb.BuildDescription) {
			hash, err := readTreehash(ctx, st, d)
			results <- result{idx: idx, hash: hash, err: err}
		}(i, desc)
	}

	var hashes [2]string
	var firstErr error
	for range descs {
		res := <-results
		hashes[res.idx] = res.hash
		// Keep only the first failure. Once it is recorded we cancel the sibling
		// read, so any later error is the cancellation we induced — discard it.
		if res.err != nil && firstErr == nil {
			firstErr = res.err
			cancel()
		}
	}
	if firstErr != nil {
		return "", "", firstErr
	}
	return hashes[0], hashes[1], nil
}

// readTreehash fetches the treehash stored at GetTreehashCachePath for the given build description.
// Returns ("", nil) on a cache miss (not-found is the normal "not yet computed" state).
// Returns ("", err) on any other storage or read failure so callers can decide whether to
// surface the error or fall back. Returns (treehash, nil) on a successful read.
func readTreehash(ctx context.Context, st storage.Storage, buildDescription *pb.BuildDescription) (string, error) {
	entityBuild, err := mapper.ProtoToBuildDescription(buildDescription)
	if err != nil {
		return "", err
	}
	key := cachekey.GetTreehashCachePath(entityBuild)
	resp, err := st.Get(ctx, storage.DownloadRequest{Key: key})
	if err != nil {
		if storage.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("treehash read failed for key %q: %w", key, err)
	}
	defer resp.ReadCloser.Close()
	b, err := io.ReadAll(resp.ReadCloser)
	if err != nil {
		return "", fmt.Errorf("treehash body read failed for key %q: %w", key, err)
	}
	return string(b), nil
}
