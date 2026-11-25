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

package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"time"

	"github.com/uber-go/tally"
	"github.com/uber/tango/config"
	"github.com/uber/tango/core/bazel"
	"github.com/uber/tango/core/cachekey"
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/itg"
	"github.com/uber/tango/core/itg/cache"
	"github.com/uber/tango/core/repomanager"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/core/targethasher"
	"github.com/uber/tango/core/workspace"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/graphrunner"
	"github.com/uber/tango/mapper"
	"github.com/uber/tango/observability/metrics"
	"go.uber.org/zap"
)

// nativeOrchestrator implements native version of Orchestrator
type nativeOrchestrator struct {
	storage     storage.Storage
	repoManager repomanager.RepoManager
	logger      *zap.Logger
	// scope is subscoped to the orchestrator component and forwarded to the
	// graph runner so its metrics nest under orchestrator.*.
	scope   tally.Scope
	emitter *metrics.Emitter
	// gitFactory allows injecting a git.Interface constructor for testing
	gitFactory  func(directory string) git.Interface
	graphRunner graphrunner.GraphRunner
	config      *config.Config
	// appCtx represents the app's overall lifetime. It is passed in by the
	// caller at construction and is expected to be cancelled when the whole
	// application is shutting down (e.g. on SIGTERM/SIGINT). Any future
	// fire-and-forget goroutines this orchestrator starts should use this
	// context instead of context.Background() so they abort promptly on
	// shutdown rather than running unbounded past server teardown.
	//
	// Per-request cancellation should still use the request's own context;
	// appCtx is only for work that intentionally outlives the request.
	appCtx context.Context
}

type Params struct {
	Storage     storage.Storage
	RepoManager repomanager.RepoManager
	Logger      *zap.Logger
	Scope       tally.Scope
	GitFactory  func(directory string) git.Interface
	GraphRunner graphrunner.GraphRunner
	Config      *config.Config // required
}

// NewNativeOrchestrator creates a new native orchestrator with the given parameters.
//
// appCtx is the application-lifetime context. Cancel it when the process is
// shutting down (e.g. wire it to SIGTERM/SIGINT in main) to abort any
// background goroutines the orchestrator spawns.
func NewNativeOrchestrator(appCtx context.Context, p Params) (Orchestrator, error) {
	if p.Config == nil {
		return nil, errors.New("config is required")
	}

	scope := p.Scope
	if scope == nil {
		scope = tally.NoopScope
	}
	scope = scope.SubScope("orchestrator")

	return &nativeOrchestrator{
		storage:     p.Storage,
		repoManager: p.RepoManager,
		logger:      p.Logger,
		scope:       scope,
		emitter:     metrics.New(scope),
		gitFactory:  p.GitFactory,
		graphRunner: p.GraphRunner,
		appCtx:      appCtx,
		config:      p.Config,
	}, nil
}

// GetTargetGraph is used to compute the target graph locally.
// It leases a workspace, checks out the base revision, applies the change requests, and computes the target graph.
func (b *nativeOrchestrator) GetTargetGraph(ctx context.Context, req entity.GetTargetGraphRequest) (_ storage.GraphReader, retErr error) {
	build := req.Build
	repoCfg, ok := b.config.GetRepositoryConfig(build.Remote)
	if !ok {
		return nil, fmt.Errorf("repository remote %q is not configured", build.Remote)
	}
	e := b.emitter.Tagged(map[string]string{metrics.TagRepo: repoCfg.RepositoryID})
	op := metrics.Begin(e, _opGetTargetGraph, metrics.SlowDurationBuckets)
	defer func() { op.Complete(retErr) }()
	logger := b.logger.With(
		zap.String("repository", repoCfg.RepositoryID),
		zap.String("base_sha", build.BaseSha),
		zap.Stringer("strategy", build.Strategy),
	)
	logger.Info("GetTargetGraph: Processing request")

	leaseStart := time.Now()
	ws, err := b.repoManager.Lease(ctx, build)
	recordStep(e, "lease_duration", leaseStart, metrics.FastDurationBuckets)
	if err != nil {
		return nil, classifyLeaseError(err)
	}
	defer func() {
		err := ws.Release()
		if err != nil {
			// clean up the workspace if release fails.
			if removeErr := os.RemoveAll(ws.Path()); removeErr != nil {
				logger.Error("GetTargetGraph: Failed to remove workspace", zap.Error(removeErr))
			}
		}
	}()
	checkoutStart := time.Now()
	err = ws.Checkout(ctx, build.Remote, build.BaseSha)
	recordStep(e, "checkout_duration", checkoutStart, metrics.FastDurationBuckets)
	if err != nil {
		return nil, classifyGitError(fmt.Errorf("checkout repository %s at %s: %w", repoCfg.RepositoryID, build.BaseSha, err))
	}
	logger.Info("GetTargetGraph: Checked out base revision")

	requests := make([]workspace.Request, 0, len(build.ChangeRequests))
	gitFactory := b.gitFactory
	if gitFactory == nil {
		gitFactory = func(dir string) git.Interface { return git.New(dir, b.logger) }
	}

	gitModule := gitFactory(ws.Path())
	for _, req := range build.ChangeRequests {
		request, err := workspace.NewRequest(req.URL, gitModule, build.Remote, build.BaseSha, logger)
		if err != nil {
			return nil, tangoerrors.NewUser(fmt.Errorf("create request for %q: %w", req.URL, err))
		}
		requests = append(requests, request)
	}
	applyStart := time.Now()
	err = ws.ApplyRequests(ctx, requests)
	recordStep(e, "apply_requests_duration", applyStart, metrics.FastDurationBuckets)
	if err != nil {
		return nil, classifyGitError(fmt.Errorf("apply requests for repository %s at %s: %w", repoCfg.RepositoryID, build.BaseSha, err))
	}
	logger.Info("GetTargetGraph: Applied requests", zap.Int("request_count", len(requests)))

	// Compute the treehash and download the target graph from storage if exists.
	treehash, err := gitModule.RevParse(ctx, "HEAD^{tree}")
	if err != nil {
		return nil, classifyGitError(fmt.Errorf("compute treehash for repository %s at %s: %w", repoCfg.RepositoryID, build.BaseSha, err))
	}
	tgbPath := cachekey.GetTGBGraphByTreeHash(repoCfg.RepositoryID, treehash, build.Strategy, req.ExcludeFilesRegex)
	if !req.BypassCache {
		cacheReadStart := time.Now()
		graphReader, err := b.readCachedGraph(ctx, logger, tgbPath)
		recordStep(e, "cache_read_duration", cacheReadStart, metrics.FastDurationBuckets)
		metrics.RecordCacheLookup(e, _opGetTargetGraph, metrics.GraphCacheLookup, err)
		if err == nil {
			logger.Info("GetTargetGraph: Cache hit on treehash", zap.String("treehash", treehash))
			return graphReader, nil
		}
		if !storage.IsNotFound(err) {
			return nil, fmt.Errorf("read graph at treehash %s: %w", treehash, err)
		}
		logger.Info("GetTargetGraph: Treehash not found, computing target graph", zap.String("treehash", treehash))
	} else {
		logger.Info("GetTargetGraph: bypass_cache=true, computing target graph")
	}
	// Store the treehash mapping in the background before the (potentially
	// slow) graph computation so concurrent or subsequent requests can
	// resolve it without waiting for the graph to finish.
	go func() {
		bgOp := metrics.Begin(e, _opTreehashCacheWrite, metrics.FastDurationBuckets)
		thCachePath := cachekey.GetTreehashCachePath(repoCfg.RepositoryID, build)
		putErr := b.storage.Put(b.appCtx, storage.UploadRequest{
			Key:    thCachePath,
			Reader: bytes.NewReader([]byte(treehash)),
		})
		bgOp.Complete(putErr)
		if putErr != nil {
			logger.Warn("GetTargetGraph: Failed to eagerly store treehash mapping",
				zap.String("path", thCachePath), zap.Error(putErr))
		} else {
			logger.Info("GetTargetGraph: Eagerly stored treehash mapping",
				zap.String("path", thCachePath), zap.String("treehash", treehash))
		}
	}()

	// Compute the target graph and store it in storage.
	result, itgProvider, err := b.computeResult(ctx, logger, e, ws, gitModule, repoCfg, req)
	if err != nil {
		return nil, err
	}
	chunks, err := mapper.ResultToGraphChunks(ctx, result, b.config.Service.MaxMessageBytes)
	if err != nil {
		return nil, fmt.Errorf("convert target graph: %w", err)
	}
	cacheWriteStart := time.Now()
	if err := storage.WriteTGBGraph(ctx, b.storage, tgbPath, chunks); err != nil {
		return nil, fmt.Errorf("write TGB graph to storage at %s: %w", tgbPath, err)
	}
	recordStep(e, "cache_write_duration", cacheWriteStart, metrics.FastDurationBuckets)
	if itgProvider != nil && len(build.ChangeRequests) == 0 {
		b.seedITGCache(logger, itgProvider, build, result)
	}
	graphReader, err := storage.NewTGBGraphReader(ctx, b.storage, tgbPath, b.config.Service.MaxMessageBytes)
	if err != nil {
		return nil, fmt.Errorf("create TGB graph reader at %s: %w", tgbPath, err)
	}
	logger.Info("GetTargetGraph: Done computing and storing target graph", zap.String("treehash", treehash))
	return graphReader, nil
}

// computeResult computes the target graph of the prepared workspace. When the
// repository enables ITG and the request has no per-request exclusions, it
// first tries the incremental path and falls back to the graph runner when that
// does not apply. It also returns the ITG provider, or nil when ITG is not in
// use, so the caller can seed the ITG cache.
func (b *nativeOrchestrator) computeResult(
	ctx context.Context,
	logger *zap.Logger,
	e *metrics.Emitter,
	ws workspace.Workspace,
	gitModule git.Interface,
	repoCfg config.RepositoryConfig,
	req entity.GetTargetGraphRequest,
) (targethasher.Result, itg.Provider, error) {
	build := req.Build
	native := build.Strategy == entity.ComputationStrategyUnset || build.Strategy == entity.ComputationStrategyNative
	// Per-request exclusions change hashes in a way a cached base graph does
	// not reflect, so those requests always use the full query.
	useITG := native && repoCfg.ITGEnabled && len(req.ExcludeFilesRegex) == 0

	var bazelClient bazel.Bazel
	if native && (b.graphRunner == nil || useITG) {
		client, err := bazel.NewBazelClient(ctx, bazel.Params{
			WorkspacePath: ws.Path(),
			Logger:        b.logger,
			BazelCommand:  repoCfg.BazelCommandPath,
			QueryTimeout:  time.Duration(repoCfg.QueryTimeoutSeconds) * time.Second,
			StreamLogs:    repoCfg.StreamBazelLogs,
		})
		if err != nil {
			return targethasher.Result{}, nil, classifyBazelClientError(err)
		}
		bazelClient = client
	}

	var itgProvider itg.Provider
	if useITG {
		itgProvider = b.newITGProvider(logger, ws.Path(), gitModule, bazelClient, repoCfg)
	}
	if itgProvider != nil {
		if result, ok := b.tryITG(ctx, logger, e, itgProvider, build); ok {
			return result, itgProvider, nil
		}
	}

	runner := b.graphRunner
	if runner == nil {
		switch build.Strategy {
		case entity.ComputationStrategyShell:
			runner = graphrunner.NewShellGraphRunner(graphrunner.ShellGraphRunnerParams{})
		case entity.ComputationStrategyUnset, entity.ComputationStrategyNative:
			runner = graphrunner.NewNativeGraphRunner(graphrunner.NativeGraphRunnerParams{
				BazelClient:        bazelClient,
				GitClient:          gitModule,
				Config:             repoCfg,
				ExtraExcludedFiles: req.ExcludeFilesRegex,
				Scope:              b.scope.Tagged(map[string]string{metrics.TagRepo: repoCfg.RepositoryID}),
			})
		default:
			return targethasher.Result{}, nil, tangoerrors.NewUser(fmt.Errorf("unknown computation strategy: %d", build.Strategy))
		}
	}
	computeStart := time.Now()
	result, err := runner.Compute(ctx, ws)
	recordStep(e, "compute_duration", computeStart, metrics.SlowDurationBuckets)
	if err != nil {
		return targethasher.Result{}, nil, fmt.Errorf("compute target graph: %w", err)
	}
	return result, itgProvider, nil
}

// newITGProvider returns an ITG provider for the workspace, or nil when it
// cannot be built. ITG is an optimization, so that is never a request failure.
func (b *nativeOrchestrator) newITGProvider(
	logger *zap.Logger,
	workspacePath string,
	gitModule git.Interface,
	bazelClient bazel.Bazel,
	repoCfg config.RepositoryConfig,
) itg.Provider {
	provider, err := itg.New(itg.Params{
		Git:              gitModule,
		Bazel:            bazelClient,
		Cache:            cache.NewStorageCache(b.storage),
		CacheNamespace:   repoCfg.RepositoryID,
		WorkspacePath:    workspacePath,
		RepositoryConfig: repoCfg,
	})
	if err != nil {
		logger.Warn("GetTargetGraph: Failed to create ITG provider, using full query", zap.Error(err))
		return nil
	}
	return provider
}

// tryITG attempts the incremental path and records its outcome. It reports
// false when the incremental path does not apply to this request, which is an
// expected outcome (for example a cold cache), not an error.
func (b *nativeOrchestrator) tryITG(
	ctx context.Context,
	logger *zap.Logger,
	e *metrics.Emitter,
	provider itg.Provider,
	build entity.BuildDescription,
) (targethasher.Result, bool) {
	e.Counter(_opGetTargetGraph, _metricITGApplicable).Inc(1)
	start := time.Now()
	result, err := provider.GetResult(ctx, itg.GetResultRequest{BaseSha: build.BaseSha, Remote: build.Remote})
	elapsed := time.Since(start)

	outcome := metrics.ResultSuccess
	if err != nil {
		outcome = _resultFallback
	}
	e.Tagged(map[string]string{metrics.TagResult: outcome}).
		DurationHistogram(_opGetTargetGraph, _metricITGComputeDuration, metrics.SlowDurationBuckets).
		RecordDuration(elapsed)
	if err != nil {
		return targethasher.Result{}, false
	}

	logger.Info("GetTargetGraph: Served by incremental target graph",
		zap.Int("target_count", len(result.Targets)),
		zap.Duration("duration", elapsed),
	)
	return result, true
}

// seedITGCache stores result as the ITG base graph for the build's base SHA in
// the background, best-effort. The write outlives the request, so it uses the
// application context. result must not be mutated after this call.
func (b *nativeOrchestrator) seedITGCache(logger *zap.Logger, provider itg.Provider, build entity.BuildDescription, result targethasher.Result) {
	go func() {
		err := provider.SeedCache(b.appCtx, itg.SeedCacheRequest{
			BaseSha: build.BaseSha,
			Remote:  build.Remote,
			Result:  result,
		})
		if err != nil {
			logger.Warn("GetTargetGraph: Failed to seed ITG cache", zap.String("base_sha", build.BaseSha), zap.Error(err))
		}
	}()
}

// readCachedGraph opens the cached TGB graph for a treehash. A TGB blob that
// exists but fails validation is treated as a miss (recompute overwrites it),
// not an infra failure, so it returns a not-found error in that case.
func (b *nativeOrchestrator) readCachedGraph(ctx context.Context, logger *zap.Logger, tgbPath string) (storage.GraphReader, error) {
	graphReader, err := storage.NewTGBGraphReader(ctx, b.storage, tgbPath, b.config.Service.MaxMessageBytes)
	if err == nil {
		return graphReader, nil
	}
	if errors.Is(err, storage.ErrCorruptTGB) {
		logger.Warn("GetTargetGraph: corrupt TGB blob, recomputing", zap.String("path", tgbPath), zap.Error(err))
		return nil, fmt.Errorf("%w: %w", storage.ErrNotFound, err)
	}
	return nil, err
}

// recordStep records a pipeline step's duration under the get_target_graph op.
func recordStep(e *metrics.Emitter, name string, start time.Time, buckets tally.DurationBuckets) {
	e.DurationHistogram(_opGetTargetGraph, name, buckets).RecordDuration(time.Since(start))
}
