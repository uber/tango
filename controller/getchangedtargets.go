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
	"maps"
	"time"

	"github.com/uber/tango/core/cachekey"
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/mapper"
	"github.com/uber/tango/internal/mapper/idmapper"
	"github.com/uber/tango/internal/streaming"
	"github.com/uber/tango/internal/targetdiff"
	"github.com/uber/tango/internal/tgb"
	"github.com/uber/tango/internal/tgbdiff"
	"github.com/uber/tango/internal/url"
	"github.com/uber/tango/observability/metrics"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/zap"
)

// fetchedGraph is one revision's fetched target graph: either an undrained
// TGB reader (preferred — the comparison path uses its random-access form
// without a full decode) or the drained chunk stream from a gob-era blob.
// Exactly one of the fields is set.
type fetchedGraph struct {
	tgb    *storage.TGBGraphReader
	chunks []entity.GetTargetGraphResponse
}

// materializeChunks returns the revision's chunk stream, paying the full
// decode when the graph was fetched as a TGB blob. Only the transitional
// mixed-format case needs this — one revision's blob predating the format
// flip — so the comparison falls back to the incumbent chunk pipeline.
func (f fetchedGraph) materializeChunks() ([]entity.GetTargetGraphResponse, error) {
	if f.tgb == nil {
		return f.chunks, nil
	}
	var chunks []entity.GetTargetGraphResponse
	for {
		chunk, err := f.tgb.Read()
		if err == io.EOF {
			return chunks, nil
		}
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
}

// job represents a single goroutine of getting a target graph
type job struct {
	graph     fetchedGraph
	err       error
	cancelled bool
	completed bool
	ctx       context.Context
	cancel    context.CancelCauseFunc
}

// GetChangedTargets returns the changed targets between two revisions. If the
// client disconnects, the stream's context is cancelled and the function
// returns with context.Canceled.
func (c *controller) GetChangedTargets(request *pb.GetChangedTargetsRequest, stream pb.TangoServiceGetChangedTargetsYARPCServer) (retErr error) {
	repo := url.ToShortRemote(request.GetFirstRevision().GetRemote())
	e := c.emitter.Tagged(map[string]string{metrics.TagRepo: repo})
	op := metrics.Begin(e, opGetChangedTargets, metrics.SlowDurationBuckets)
	logger := c.logger.WithLazy(
		zap.Any("first_revision", request.GetFirstRevision()),
		zap.Any("second_revision", request.GetSecondRevision()),
	)
	defer func() {
		op.Complete(retErr)
		if retErr != nil {
			logger.Error("GetChangedTargets failed", tangoerrors.Fields(retErr)...)
			retErr = toWireError(retErr)
		}
	}()
	if err := validateGetChangedTargetsRequest(request); err != nil {
		return tangoerrors.NewUser(err)
	}
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
		served, err := c.serveChangedTargetsFromCache(ctx, e, logger, request, stream, maxDist, start)
		if err != nil {
			return fmt.Errorf("serve from cache: %w", err)
		}
		if served {
			return nil
		}
	}

	// Fetch both revisions' target graphs concurrently.
	firstGraph, secondGraph, err := c.fetchTargetGraphs(ctx, e, logger, request)
	if err != nil {
		return fmt.Errorf("fetch target graphs: %w", err)
	}

	changedTargetsResponses, err := c.compareFetchedGraphs(ctx, e, logger, firstGraph, secondGraph, c.seedAttributesFor(request.GetFirstRevision().GetRemote()))
	// Allow GC of raw graph data while the caching goroutine runs.
	firstGraph = fetchedGraph{}
	secondGraph = fetchedGraph{}
	if err != nil {
		if ctx.Err() != nil {
			err = context.Cause(ctx)
		}
		return fmt.Errorf("compare target graphs: %w", err)
	}

	// Cache the computed result concurrently so it doesn't block the stream send.
	c.cacheComparedTargets(logger, request, changedTargetsResponses)

	sendStart := time.Now()
	if err := sendTrimmedChangedTargets(stream, changedTargetsResponses, maxDist, request.GetOutputConfig()); err != nil {
		return fmt.Errorf("send response: %w", err)
	}
	sendDuration := time.Since(sendStart)
	e.DurationHistogram(opGetChangedTargets, "send_duration", metrics.FastDurationBuckets).RecordDuration(sendDuration)

	logger.Info("GetChangedTargets: Successfully processed request",
		zap.Duration("send_duration", sendDuration),
		zap.Duration("total_duration", time.Since(start)),
	)
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
func (c *controller) serveChangedTargetsFromCache(ctx context.Context, e *metrics.Emitter, logger *zap.Logger, request *pb.GetChangedTargetsRequest, stream pb.TangoServiceGetChangedTargetsYARPCServer, maxDist int32, start time.Time) (bool, error) {
	cacheStart := time.Now()
	treehash1, treehash2, err := readTreehashParallel(ctx, c.storage, request.GetFirstRevision(), request.GetSecondRevision(), e, opGetChangedTargets)
	if err != nil {
		return false, fmt.Errorf("read revision treehash: %w", err)
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
		metrics.RecordCacheLookup(e, opGetChangedTargets, metrics.ComparedTargetsCacheLookup, cacheErr)
		return false, nil
	}

	// Buffer all responses before sending any. A concurrent goroutine write may have
	// left a partial blob in storage; buffering lets us detect corruption and fall
	// through to recompute before we've sent anything to the client.
	var cached []entity.GetChangedTargetsResponse
	var readErr error
	for {
		if err := ctx.Err(); err != nil {
			_ = cachedReader.Close()
			// Client gave up while we were draining the cache. Surface as a user-cancelled error.
			return false, fmt.Errorf("cache reader: %w", context.Cause(ctx))
		}
		var resp entity.GetChangedTargetsResponse
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
	_ = cachedReader.Close()

	if readErr != nil {
		// Blob is corrupt (likely an incomplete write); recompute.
		logger.Warn("GetChangedTargets: Cached result is incomplete, recomputing", zap.Error(readErr))
		return false, nil
	}

	cacheReadDuration := time.Since(cacheStart)
	logger.Info("GetChangedTargets: Cache hit, streaming from storage",
		zap.Duration("cache_read_duration", cacheReadDuration),
	)
	metrics.RecordCacheLookup(e, opGetChangedTargets, metrics.ComparedTargetsCacheLookup, nil)
	e.DurationHistogram(opGetChangedTargets, "cache_read_duration", metrics.FastDurationBuckets).RecordDuration(cacheReadDuration)
	if sendErr := sendTrimmedChangedTargets(stream, cached, maxDist, request.GetOutputConfig()); sendErr != nil {
		return false, fmt.Errorf("send cached response: %w", sendErr)
	}
	logger.Info("GetChangedTargets: Successfully streamed from cache",
		zap.Duration("total_duration", time.Since(start)),
	)
	return true, nil
}

// fetchTargetGraphs fetches both revisions' target graphs concurrently. Each
// fetch runs under its own cancellable context so that, when one fails, the
// sibling is cancelled to avoid wasting work on a result that will be discarded.
// Errors caused solely by that induced cancellation are dropped; only the
// original failure is returned. A client disconnect surfaces as a user-cancelled
// error. A graph stored as a TGB blob comes back as its undrained reader; a
// gob-era graph is drained into chunks here, inside the concurrent fetch.
func (c *controller) fetchTargetGraphs(ctx context.Context, e *metrics.Emitter, logger *zap.Logger, request *pb.GetChangedTargetsRequest) (fetchedGraph, fetchedGraph, error) {
	jobs := make([]*job, 2)
	for i := 0; i < 2; i++ {
		// create independent contexts for each job; if one of the jobs fails, the other one should be cancelled to save resources and improve latency
		ctxNew, cancelNew := context.WithCancelCause(ctx)
		defer cancelNew(nil)
		jobs[i] = &job{ctx: ctxNew, cancel: cancelNew}
	}

	// Start jobs for both revisions. Success or failure, the result will report to the results channel.
	type graphResult struct {
		// order is 0 or 1, 0 is the base (first) revision, 1 is the target (second) revision
		order int
		graph fetchedGraph
		err   error
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
			graphReader, err := c.getGraph(jobs[idx].ctx, e, entityReq)
			if err != nil || graphReader == nil {
				results <- graphResult{order: idx, err: err}
				return
			}
			// A TGB-backed reader is handed over whole: the comparison path
			// wants its random-access form, and draining it here would force
			// the full decode the format exists to avoid.
			if tgbReader, ok := graphReader.(*storage.TGBGraphReader); ok {
				results <- graphResult{order: idx, graph: fetchedGraph{tgb: tgbReader}}
				return
			}
			defer func() { _ = graphReader.Close() }()

			var chunks []entity.GetTargetGraphResponse
			for {
				chunk, err := graphReader.Read()
				if err == io.EOF {
					results <- graphResult{order: idx, graph: fetchedGraph{chunks: chunks}}
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
		jobs[res.order].graph = res.graph
		jobs[res.order].completed = true
		jobs[res.order].err = res.err
		if res.graph.chunks == nil && res.graph.tgb == nil && res.err == nil {
			jobs[res.order].err = errors.New("no chunks returned")
		}

		// one of the computations failed, if the other one has not
		// completed yet, cancel it and wait for the result to come in,
		// which would be a context cancelled result then
		if jobs[res.order].err != nil {
			other := (res.order + 1) % 2
			if !jobs[other].completed {
				jobs[other].cancel(fmt.Errorf("cancelled: sibling graph #%d failed: %w", res.order+1, jobs[res.order].err))
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
	e.DurationHistogram(opGetChangedTargets, "graph_fetch_duration", metrics.SlowDurationBuckets).RecordDuration(graphFetchDuration)

	if ctx.Err() != nil {
		// If the context was cancelled by the upstream, just return the original error without additional augmentation
		return fetchedGraph{}, fetchedGraph{}, context.Cause(ctx)
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
		return fetchedGraph{}, fetchedGraph{}, err
	}

	firstGraph := jobs[0].graph
	secondGraph := jobs[1].graph
	// Drop job references so the GC can reclaim them once the comparison is done.
	jobs[0].graph = fetchedGraph{}
	jobs[1].graph = fetchedGraph{}
	return firstGraph, secondGraph, nil
}

// cacheComparedTargets writes the computed compared-targets result to storage in
// a fire-and-forget goroutine so it does not block the stream send. The responses
// is only read (never mutated) by the goroutine and the foreground send, so
// concurrent access is safe; the caller must not mutate it. This is best effort.
func (c *controller) cacheComparedTargets(logger *zap.Logger, request *pb.GetChangedTargetsRequest, responses []entity.GetChangedTargetsResponse) {
	go func() {
		// Use c.appCtx directly: the cache write is fire-and-forget and must
		// outlive the request (so a client disconnect doesn't abort it) but
		// must NOT outlive the server (so it doesn't leak past shutdown).
		// c.appCtx fits both: it's never cancelled by client disconnect and
		// is cancelled on shutdown. Per-operation deadlines are the storage
		// backend's responsibility — the controller is backend-agnostic and
		// must not encode any one implementation's I/O budget.
		// The treehash reads here are for building the write key, not a cache
		// serve attempt, so they pass a no-op emitter to avoid skewing the
		// treehash cache hit rate.
		treehash1, treehash2, err := readTreehashParallel(c.appCtx, c.storage, request.GetFirstRevision(), request.GetSecondRevision(), metrics.Nop(), opGetChangedTargets)
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

// compareFetchedGraphs picks the comparison path for a pair of fetched
// graphs: TGB-native when both revisions came back as TGB blobs, otherwise
// the incumbent chunk pipeline (gob blobs, or the transitional mixed case
// where exactly one revision's blob predates a format flip).
func (c *controller) compareFetchedGraphs(ctx context.Context, e *metrics.Emitter, logger *zap.Logger, first, second fetchedGraph, seedAttrs map[string]bool) ([]entity.GetChangedTargetsResponse, error) {
	if first.tgb != nil && second.tgb != nil {
		firstATFH, err := first.tgb.TGB().AllTargetsFileHashes()
		if err != nil {
			return nil, fmt.Errorf("read AllTargetsFileHashes from first TGB: %w", err)
		}
		secondATFH, err := second.tgb.TGB().AllTargetsFileHashes()
		if err != nil {
			return nil, fmt.Errorf("read AllTargetsFileHashes from second TGB: %w", err)
		}
		if allTargetsFileChanged(
			&entity.Metadata{AllTargetsFileHashes: firstATFH},
			&entity.Metadata{AllTargetsFileHashes: secondATFH},
		) {
			logger.Info("compareFetchedGraphs: AllTargetsFiles trigger matched (TGB), reporting all targets as changed")
			e.Counter(opGetChangedTargets, "all_targets_triggered").Inc(1)
			return c.allTargetsChangedFromTGB(ctx, second.tgb.TGB())
		}
		return c.compareTargetGraphsTGB(ctx, e, logger, first.tgb.TGB(), second.tgb.TGB(), seedAttrs)
	}
	firstChunks, err := first.materializeChunks()
	if err != nil {
		return nil, fmt.Errorf("decode first graph: %w", err)
	}
	secondChunks, err := second.materializeChunks()
	if err != nil {
		return nil, fmt.Errorf("decode second graph: %w", err)
	}
	return c.compareTargetGraphs(ctx, e, logger, firstChunks, secondChunks, seedAttrs)
}

// compareTargetGraphsTGB diffs two TGB-backed graphs without materialising
// either into a semantic graph: tgbdiff.Compare works on the readers'
// columnar form directly, and only the changed targets are materialised into
// the targetdiff.Result shape the response pipeline consumes. With
// ShadowCompare on, the incumbent targetdiff comparison additionally runs
// over the same two readers in a background goroutine and any divergence is
// logged and counted (see shadowCompareTGB).
func (c *controller) compareTargetGraphsTGB(ctx context.Context, e *metrics.Emitter, logger *zap.Logger, before, after *tgb.Reader, seedAttrs map[string]bool) ([]entity.GetChangedTargetsResponse, error) {
	compareStart := time.Now()
	defer func() {
		e.DurationHistogram(opGetChangedTargets, "compare_duration", metrics.SlowDurationBuckets).RecordDuration(time.Since(compareStart))
	}()
	logger.Info("compareTargetGraphsTGB: Computing differences between target graphs")
	e.Counter(opGetChangedTargets, "tgb_native_compare").Inc(1)

	diffStart := time.Now()
	res, err := tgbdiff.Compare(ctx, before, after, tgbdiff.Options{
		MaxDistance: -1,
		SeedAttrs:   seedAttrs,
	})
	if err != nil {
		return nil, fmt.Errorf("tgb compare: %w", err)
	}
	e.DurationHistogram(opGetChangedTargets, "diff_duration", metrics.FastDurationBuckets).RecordDuration(time.Since(diffStart))

	materializeStart := time.Now()
	result, err := tgbdiff.Materialize(before, after, res, seedAttrs)
	if err != nil {
		return nil, fmt.Errorf("materialize changed targets: %w", err)
	}
	e.DurationHistogram(opGetChangedTargets, "materialize_duration", metrics.FastDurationBuckets).RecordDuration(time.Since(materializeStart))
	e.ValueHistogram(opGetChangedTargets, "target_count", metrics.LargeCountBuckets).RecordValue(float64(len(result.ChangedTargets)))

	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}

	if c.shadowCompare {
		// The goroutine only reads the readers and the result, and the
		// remainder of the request only reads the result, so handing both
		// over without copies is safe.
		c.shadowCompareTGB(logger, e, before, after, seedAttrs, result)
	}

	responses, err := c.resultToResponses(result)
	if err != nil {
		return nil, err
	}
	logger.Info("GetChangedTargets: Target graphs compared (TGB)")
	return responses, nil
}

// shadowCompareTGB runs the incumbent targetdiff comparison over the same two
// TGB readers in a fire-and-forget goroutine and emits a mismatch metric plus
// a detailed log when the TGB path's result diverges. This is the
// production-scale extension of the internal/tgbdiff differential tests: while
// it stays clean, any tango-vs-incumbent-system delta is known to be
// algorithmic rather than a codec artifact. It never affects what is served.
//
// Runs under c.appCtx: the oracle should finish even when the client has
// disconnected (the signal is about the comparison, not the request) but must
// die at process shutdown.
func (c *controller) shadowCompareTGB(logger *zap.Logger, e *metrics.Emitter, before, after *tgb.Reader, seedAttrs map[string]bool, got targetdiff.Result) {
	go func() {
		start := time.Now()
		oracle, err := targetdiff.Compare(c.appCtx, targetdiff.Request{
			Before:      tgbdiff.SemanticGraph(before, seedAttrs),
			After:       tgbdiff.SemanticGraph(after, seedAttrs),
			MaxDistance: -1,
		})
		if err != nil {
			e.Counter(opGetChangedTargets, "tgb_shadow_error").Inc(1)
			logger.Warn("GetChangedTargets: TGB shadow compare failed", zap.Error(err))
			return
		}
		equivalent, divergence := tgbdiff.ResultsEquivalent(got, oracle)
		if !equivalent {
			e.Counter(opGetChangedTargets, "tgb_shadow_mismatch").Inc(1)
			logger.Error("GetChangedTargets: TGB comparison diverges from targetdiff oracle",
				zap.String("divergence", divergence),
				zap.Duration("shadow_duration", time.Since(start)))
			return
		}
		e.Counter(opGetChangedTargets, "tgb_shadow_match").Inc(1)
		logger.Info("GetChangedTargets: TGB shadow compare matched",
			zap.Duration("shadow_duration", time.Since(start)))
	}()
}

// compareTargetGraphs diffs two target graph streams and produces a chunked
// GetChangedTargetsResponse stream. Each stream is decoded into a semantic
// targetdiff.Graph (int32 IDs resolved to names via that stream's metadata),
// the two graphs are compared by internal/targetdiff, and the resulting changes
// are re-mapped into a canonical per-call ID namespace so the response metadata
// only carries the names actually referenced. See internal/targetdiff for the
// classification and distance rules.
func (c *controller) compareTargetGraphs(ctx context.Context, e *metrics.Emitter, logger *zap.Logger, firstGraph, secondGraph []entity.GetTargetGraphResponse, seedAttrs map[string]bool) ([]entity.GetChangedTargetsResponse, error) {
	compareStart := time.Now()
	defer func() {
		e.DurationHistogram(opGetChangedTargets, "compare_duration", metrics.SlowDurationBuckets).RecordDuration(time.Since(compareStart))
	}()
	logger.Info("compareTargetGraphs: Computing differences between target graphs")

	// 1) Decode each stream into a semantic graph keyed by canonical target name.
	decodeStart := time.Now()
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

	if allTargetsFileChanged(firstMetadata, secondMetadata) {
		logger.Info("compareTargetGraphs: AllTargetsFiles trigger matched, reporting all targets as changed")
		e.Counter(opGetChangedTargets, "all_targets_triggered").Inc(1)
		return c.allTargetsChangedFromGraph(ctx, secondTargetsByID, secondMetadata)
	}

	before, err := toDiffGraph(ctx, firstTargetsByID, firstMetadata, seedAttrs)
	if err != nil {
		return nil, err
	}
	// Metadata and ID map are fully consumed by the name-resolved graph; drop them.
	firstTargetsByID = nil
	firstMetadata = nil
	after, err := toDiffGraph(ctx, secondTargetsByID, secondMetadata, seedAttrs)
	if err != nil {
		return nil, err
	}
	secondTargetsByID = nil
	secondMetadata = nil
	e.DurationHistogram(opGetChangedTargets, "decode_duration", metrics.FastDurationBuckets).RecordDuration(time.Since(decodeStart))

	// 2) Compare the two semantic graphs.
	diffStart := time.Now()
	result, err := targetdiff.Compare(ctx, targetdiff.Request{
		Before:      before,
		After:       after,
		MaxDistance: -1,
	})
	if err != nil {
		return nil, err
	}
	// Release the input graphs; only result is needed from here on.
	before = nil
	after = nil
	e.DurationHistogram(opGetChangedTargets, "diff_duration", metrics.FastDurationBuckets).RecordDuration(time.Since(diffStart))
	e.ValueHistogram(opGetChangedTargets, "target_count", metrics.LargeCountBuckets).RecordValue(float64(len(result.ChangedTargets)))

	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}

	// 3) Re-map into the canonical response shape.
	results, err := c.resultToResponses(result)
	if err != nil {
		return nil, err
	}
	logger.Info("GetChangedTargets: Target graphs compared")
	return results, nil
}

// resultToResponses re-maps each change into a canonical per-call ID
// namespace and splits the result into message-size-bounded response chunks.
// The mappers only assign IDs to names they actually see, so the emitted
// metadata is pruned to what the changed targets reference.
func (c *controller) resultToResponses(result targetdiff.Result) ([]entity.GetChangedTargetsResponse, error) {
	mappers := newCanonicalMappers()
	changed := make([]entity.ChangedTarget, 0, len(result.ChangedTargets))
	for _, ct := range result.ChangedTargets {
		changed = append(changed, entity.ChangedTarget{
			ChangeType: toChangeType(ct.ChangeType),
			OldTarget:  mappers.transpose(ct.Before),
			NewTarget:  mappers.transpose(ct.After),
			Distance:   ct.Distance,
		})
	}

	changedGroups, err := streaming.SplitBySize(changed, c.maxMessageBytes)
	if err != nil {
		return nil, err
	}
	results := make([]entity.GetChangedTargetsResponse, 0, len(changedGroups))
	for _, g := range changedGroups {
		results = append(results, entity.GetChangedTargetsResponse{ChangedTargets: g})
	}
	metaGroups, err := streaming.SplitMetadata(
		mappers.target.Invert(),
		mappers.ruleType.Invert(),
		mappers.tag.Invert(),
		mappers.attrName.Invert(),
		mappers.attrVal.Invert(),
		c.maxMessageBytes,
	)
	if err != nil {
		return nil, err
	}
	for _, m := range metaGroups {
		results = append(results, entity.GetChangedTargetsResponse{Metadata: m})
	}
	return results, nil
}

// cancelCheckInterval is how often long-running loops check ctx.Err().
const cancelCheckInterval = 4096

// getTargetsAndMetadata builds ID->target maps and merges metadata from a target graph stream.
// Metadata may arrive in multiple chunks (e.g. when target_id_mapping exceeds the gRPC message
// size limit); all chunks are merged into a single Metadata so callers can use it uniformly.
func getTargetsAndMetadata(ctx context.Context, graph []entity.GetTargetGraphResponse) (map[int32]*entity.OptimizedTarget, *entity.Metadata, error) {
	targets := make(map[int32]*entity.OptimizedTarget)
	merged := &entity.Metadata{
		TargetIDMapping:             make(map[int32]string),
		RuleTypeMapping:             make(map[int32]string),
		TagMapping:                  make(map[int32]string),
		AttributeNameMapping:        make(map[int32]string),
		AttributeStringValueMapping: make(map[int32]string),
	}
	for _, chunk := range graph {
		if ctx.Err() != nil {
			return nil, nil, context.Cause(ctx)
		}
		for i := range chunk.Targets {
			t := &chunk.Targets[i]
			targets[t.ID] = t
		}
		if m := chunk.Metadata; m != nil {
			for k, v := range m.TargetIDMapping {
				merged.TargetIDMapping[k] = v
			}
			for k, v := range m.RuleTypeMapping {
				merged.RuleTypeMapping[k] = v
			}
			for k, v := range m.TagMapping {
				merged.TagMapping[k] = v
			}
			for k, v := range m.AttributeNameMapping {
				merged.AttributeNameMapping[k] = v
			}
			for k, v := range m.AttributeStringValueMapping {
				merged.AttributeStringValueMapping[k] = v
			}
			for k, v := range m.AllTargetsFileHashes {
				if merged.AllTargetsFileHashes == nil {
					merged.AllTargetsFileHashes = make(map[string]string, len(m.AllTargetsFileHashes))
				}
				merged.AllTargetsFileHashes[k] = v
			}
		}
	}
	return targets, merged, nil
}

// allTargetsFileChanged reports whether the complete configured
// AllTargetsFileHashes state differs between the two metadata sets.
func allTargetsFileChanged(first, second *entity.Metadata) bool {
	return !maps.Equal(first.AllTargetsFileHashes, second.AllTargetsFileHashes)
}

// allTargetsChangedFromGraph builds a GetChangedTargetsResponse stream that
// marks every target in the second graph as ChangeTypeChanged with distance 0.
// Used when an AllTargetsFiles trigger fires.
func (c *controller) allTargetsChangedFromGraph(ctx context.Context, targetsByID map[int32]*entity.OptimizedTarget, meta *entity.Metadata) ([]entity.GetChangedTargetsResponse, error) {
	graph, err := toDiffGraph(ctx, targetsByID, meta, nil)
	if err != nil {
		return nil, err
	}
	changed := make([]*targetdiff.ChangedTarget, 0, len(graph))
	for _, target := range graph {
		changed = append(changed, &targetdiff.ChangedTarget{
			ChangeType: targetdiff.ChangeTypeChanged,
			After:      target,
		})
	}
	return c.resultToResponses(targetdiff.Result{ChangedTargets: changed})
}

// allTargetsChangedFromTGB builds a response stream marking every target in
// the TGB reader's graph as changed with distance 0. Used when the
// AllTargetsFiles trigger fires on the TGB comparison path.
func (c *controller) allTargetsChangedFromTGB(ctx context.Context, r *tgb.Reader) ([]entity.GetChangedTargetsResponse, error) {
	g, err := r.DecodeGraph()
	if err != nil {
		return nil, fmt.Errorf("decode TGB graph: %w", err)
	}
	targetsByID := make(map[int32]*entity.OptimizedTarget, len(g.Targets))
	for i := range g.Targets {
		t := &g.Targets[i]
		targetsByID[t.ID] = t
	}
	return c.allTargetsChangedFromGraph(ctx, targetsByID, &g.Metadata)
}

// seedAttributesFor returns the RepositoryConfig.SeedAttributes
// allowlist configured for the given remote, or nil when the repository has no
// override configured — meaning every attribute is considered a valid signal
// for a direct-change classification (no filtering). See
// RepositoryConfig.SeedAttributes for the full rationale, and
// attributesChanged in internal/targetdiff/compare.go for how the allowlist
// is applied.
func (c *controller) seedAttributesFor(remote string) map[string]bool {
	if c.repoConfig == nil {
		return nil
	}
	cfg, ok := c.repoConfig.GetRepositoryConfig(remote)
	if !ok || len(cfg.SeedAttributes) == 0 {
		return nil
	}
	set := make(map[string]bool, len(cfg.SeedAttributes))
	for _, attr := range cfg.SeedAttributes {
		set[attr] = true
	}
	return set
}

// toDiffGraph resolves a stream's int32 IDs into a semantic targetdiff.Graph
// keyed by canonical target name. Targets with no name mapping are skipped;
// dependency, tag, and attribute IDs that don't resolve are dropped. When
// seedAttrs is nil, every attribute is kept; otherwise only attributes
// present in seedAttrs are kept on each target.
func toDiffGraph(ctx context.Context, targetsByID map[int32]*entity.OptimizedTarget, meta *entity.Metadata, seedAttrs map[string]bool) (targetdiff.Graph, error) {
	targetIDMap := meta.TargetIDMapping
	ruleTypeMap := meta.RuleTypeMapping
	tagMap := meta.TagMapping
	attrNameMap := meta.AttributeNameMapping
	attrValMap := meta.AttributeStringValueMapping

	graph := make(targetdiff.Graph, len(targetsByID))
	i := 0
	for id, t := range targetsByID {
		if i%cancelCheckInterval == 0 && ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		i++
		name := targetIDMap[id]
		if name == "" {
			continue
		}
		target := &targetdiff.Target{
			Name:     name,
			Hash:     t.Hash,
			RuleType: ruleTypeMap[t.RuleType],
			Root:     t.Root,
			External: t.External,
		}
		if deps := t.DirectDependencies; len(deps) > 0 {
			target.Dependencies = make([]string, 0, len(deps))
			for _, depID := range deps {
				if depName := targetIDMap[depID]; depName != "" {
					target.Dependencies = append(target.Dependencies, depName)
				}
			}
		}
		if tags := t.Tags; len(tags) > 0 {
			target.Tags = make([]string, 0, len(tags))
			for _, tagID := range tags {
				if tagName := tagMap[tagID]; tagName != "" {
					target.Tags = append(target.Tags, tagName)
				}
			}
		}
		if attrs := t.Attributes; len(attrs) > 0 {
			target.Attributes = make(map[string]string, len(attrs))
			for nameID, valID := range attrs {
				attrName := attrNameMap[nameID]
				if attrName == "" {
					continue
				}
				if seedAttrs != nil && !seedAttrs[attrName] {
					continue
				}
				target.Attributes[attrName] = attrValMap[valID]
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

// transpose converts a semantic targetdiff.Target into an entity OptimizedTarget,
// assigning canonical IDs to every name it references. Returns nil for a nil src.
func (m *canonicalMappers) transpose(src *targetdiff.Target) *entity.OptimizedTarget {
	if src == nil {
		return nil
	}
	dst := &entity.OptimizedTarget{
		ID:       m.target.ID(src.Name),
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

// toChangeType maps a targetdiff.ChangeType to its entity equivalent.
func toChangeType(ct targetdiff.ChangeType) entity.ChangeType {
	switch ct {
	case targetdiff.ChangeTypeNew:
		return entity.ChangeTypeNew
	case targetdiff.ChangeTypeDeleted:
		return entity.ChangeTypeDeleted
	case targetdiff.ChangeTypeChanged:
		return entity.ChangeTypeChanged
	default:
		return entity.ChangeTypeInvalid
	}
}

// sendTrimmedChangedTargets streams responses to the client, filtering changed targets to those
// within maxDist from any distance-0 seed when maxDist >= 0, stripping per-target
// hash/tags/attributes per outputConfig's include_* flags, and pruning metadata mappings
// whose IDs are no longer referenced. Each entity response is converted to proto at the
// stream.Send boundary.
func sendTrimmedChangedTargets(stream pb.TangoServiceGetChangedTargetsYARPCServer, responses []entity.GetChangedTargetsResponse, maxDist int32, outputConfig *pb.OutputConfig) error {
	stripFields := optimizedTargetNeedsStripping(outputConfig)
	pruneMeta := metadataNeedsPruning(outputConfig)
	for i := range responses {
		protoResp := mapper.ChangedTargetsResponseToProto(&responses[i])
		toSend := protoResp
		switch item := protoResp.GetItem().(type) {
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
func readTreehashParallel(ctx context.Context, st storage.Storage, first, second *pb.BuildDescription, e *metrics.Emitter, op string) (string, string, error) {
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
			hash, err := readTreehash(ctx, st, d, e, op)
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
func readTreehash(ctx context.Context, st storage.Storage, buildDescription *pb.BuildDescription, e *metrics.Emitter, op string) (string, error) {
	entityBuild, err := mapper.ProtoToBuildDescription(buildDescription)
	if err != nil {
		return "", err
	}
	key := cachekey.GetTreehashCachePath(entityBuild)
	resp, err := st.Get(ctx, storage.DownloadRequest{Key: key})
	metrics.RecordCacheLookup(e, op, metrics.TreehashCacheLookup, err)
	if err != nil {
		if storage.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("treehash read failed for key %q: %w", key, err)
	}
	defer func() { _ = resp.ReadCloser.Close() }()
	b, err := io.ReadAll(resp.ReadCloser)
	if err != nil {
		return "", fmt.Errorf("treehash body read failed for key %q: %w", key, err)
	}
	return string(b), nil
}
