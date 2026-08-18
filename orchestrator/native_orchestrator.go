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
	"io"
	"os"

	"time"

	"github.com/uber-go/tally"
	"github.com/uber/tango/config"
	"github.com/uber/tango/core/bazel"
	"github.com/uber/tango/core/cachekey"
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/repomanager"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/core/workspace"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/graphrunner"
	"github.com/uber/tango/internal/url"
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
	e := b.emitter.Tagged(map[string]string{metrics.TagRepo: url.ToShortRemote(req.Build.Remote)})
	op := metrics.Begin(e, _opGetTargetGraph, metrics.SlowDurationBuckets)
	defer func() { op.Complete(retErr) }()
	build := req.Build
	logger := b.logger.With(zap.Any("build_description", build))
	logger.Info("GetTargetGraph: Processing request")

	remote := build.Remote
	repoCfg, ok := b.config.GetRepositoryConfig(remote)
	if !ok {
		return nil, fmt.Errorf("no repository configuration found for remote %q", remote)
	}
	leaseStart := time.Now()
	ws, err := b.repoManager.Lease(ctx, build)
	recordStep(e, _opGetTargetGraph, "lease_duration", leaseStart, metrics.FastDurationBuckets)
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
	gitFactory := b.gitFactory
	if gitFactory == nil {
		gitFactory = func(dir string) git.Interface { return git.New(dir, b.logger) }
	}
	gitModule := gitFactory(ws.Path())

	treehash, err := b.materializeTreehash(ctx, e, ws, gitModule, build, logger)
	if err != nil {
		return nil, err
	}

	// Compute the treehash and download the target graph from storage if exists.
	treehashPath := cachekey.GetGraphByTreeHash(build.Remote, treehash, build.Strategy, req.ExcludeFilesRegex)
	useTGB := b.config.Service.GraphFormat == config.GraphFormatTGB
	tgbPath := cachekey.GetTGBGraphByTreeHash(build.Remote, treehash, build.Strategy, req.ExcludeFilesRegex)
	if !req.BypassCache {
		cacheReadStart := time.Now()
		graphReader, err := b.readCachedGraph(ctx, logger, useTGB, tgbPath, treehashPath)
		recordStep(e, _opGetTargetGraph, "cache_read_duration", cacheReadStart, metrics.FastDurationBuckets)
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
		thCachePath := cachekey.GetTreehashCachePath(build)
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
	runner := b.graphRunner
	if runner == nil {
		switch build.Strategy {
		case entity.ComputationStrategyShell:
			runner = graphrunner.NewShellGraphRunner(graphrunner.ShellGraphRunnerParams{})
		case entity.ComputationStrategyUnset, entity.ComputationStrategyNative:
			client, err := bazel.NewBazelClient(ctx, bazel.Params{
				WorkspacePath: ws.Path(),
				Logger:        b.logger,
				BazelCommand:  repoCfg.BazelCommandPath,
				QueryTimeout:  time.Duration(repoCfg.QueryTimeoutSeconds) * time.Second,
				StreamLogs:    repoCfg.StreamBazelLogs,
			})
			if err != nil {
				return nil, classifyBazelClientError(err)
			}
			runner = graphrunner.NewNativeGraphRunner(graphrunner.NativeGraphRunnerParams{
				BazelClient:        client,
				GitClient:          gitModule,
				Config:             repoCfg,
				ExtraExcludedFiles: req.ExcludeFilesRegex,
				Scope:              b.scope.Tagged(map[string]string{metrics.TagRepo: url.ToShortRemote(build.Remote)}),
			})
		default:
			return nil, tangoerrors.NewUser(fmt.Errorf("unknown computation strategy: %d", build.Strategy))
		}
	}
	computeStart := time.Now()
	result, err := runner.Compute(ctx, ws)
	recordStep(e, _opGetTargetGraph, "compute_duration", computeStart, metrics.SlowDurationBuckets)
	if err != nil {
		return nil, fmt.Errorf("compute target graph: %w", err)
	}
	chunks, err := mapper.ResultToGraphChunks(ctx, result, b.config.Service.MaxMessageBytes)
	if err != nil {
		return nil, fmt.Errorf("convert target graph: %w", err)
	}
	cacheWriteStart := time.Now()
	if useTGB {
		if err := storage.WriteTGBGraph(ctx, b.storage, tgbPath, chunks); err != nil {
			return nil, fmt.Errorf("write TGB graph to storage at %s: %w", tgbPath, err)
		}
		recordStep(e, _opGetTargetGraph, "cache_write_duration", cacheWriteStart, metrics.FastDurationBuckets)
		graphReader, err := storage.NewTGBGraphReader(ctx, b.storage, tgbPath, b.config.Service.MaxMessageBytes)
		if err != nil {
			return nil, fmt.Errorf("create TGB graph reader at %s: %w", tgbPath, err)
		}
		logger.Info("GetTargetGraph: Done computing and storing target graph", zap.String("treehash", treehash))
		return graphReader, nil
	}
	err = storage.WriteGraphStream(ctx, b.storage, treehashPath, chunks)
	if err != nil {
		return nil, fmt.Errorf("write graph to storage at %s: %w", treehashPath, err)
	}
	recordStep(e, _opGetTargetGraph, "cache_write_duration", cacheWriteStart, metrics.FastDurationBuckets)
	graphReader, err := storage.NewGraphReader(ctx, b.storage, treehashPath)
	if err != nil {
		return nil, fmt.Errorf("create graph reader at %s: %w", treehashPath, err)
	}
	logger.Info("GetTargetGraph: Done computing and storing target graph", zap.String("treehash", treehash))
	return graphReader, nil
}

// materializeTreehash checks out build.BaseSha in ws, applies build.ChangeRequests
// on top, and returns the resulting git tree hash. gitModule must be rooted at
// ws.Path(). Shared by GetTargetGraph and HasAllTargetsFileChange so both
// compute a revision's materialized tree identically.
func (b *nativeOrchestrator) materializeTreehash(ctx context.Context, e *metrics.Emitter, ws workspace.Workspace, gitModule git.Interface, build entity.BuildDescription, logger *zap.Logger) (string, error) {
	checkoutStart := time.Now()
	err := ws.Checkout(ctx, build.Remote, build.BaseSha)
	recordStep(e, _opGetTargetGraph, "checkout_duration", checkoutStart, metrics.FastDurationBuckets)
	if err != nil {
		return "", classifyGitError(fmt.Errorf("checkout %s@%s: %w", build.Remote, build.BaseSha, err))
	}
	logger.Info("materializeTreehash: Checked out base revision")

	requests := make([]workspace.Request, 0, len(build.ChangeRequests))
	for _, req := range build.ChangeRequests {
		request, err := workspace.NewRequest(req.URL, gitModule, build.Remote, build.BaseSha, logger)
		if err != nil {
			return "", tangoerrors.NewUser(fmt.Errorf("create request for %q: %w", req.URL, err))
		}
		requests = append(requests, request)
	}
	applyStart := time.Now()
	err = ws.ApplyRequests(ctx, requests)
	recordStep(e, _opGetTargetGraph, "apply_requests_duration", applyStart, metrics.FastDurationBuckets)
	if err != nil {
		return "", classifyGitError(fmt.Errorf("apply requests for %s@%s: %w", build.Remote, build.BaseSha, err))
	}
	logger.Info("materializeTreehash: Applied requests", zap.Int("request_count", len(requests)))

	treehash, err := gitModule.RevParse(ctx, "HEAD^{tree}")
	if err != nil {
		return "", classifyGitError(fmt.Errorf("compute treehash for %s@%s: %w", build.Remote, build.BaseSha, err))
	}
	return treehash, nil
}

// HasAllTargetsFileChange reports whether a file configured in the
// repository's RepositoryConfig.AllTargetsFiles list differs between first
// and second. It looks up the repository config by first.Remote; when the
// repository has no AllTargetsFiles configured, it returns (false, nil)
// without leasing a workspace or touching storage.
//
// When both revisions' treehashes and a prior result for that pair are
// already cached, the answer is served from cache. Otherwise a workspace is
// leased once and both revisions are materialized into it in turn (see
// materializeTreehash), so their tree objects are guaranteed to coexist in
// the same git object database — required to diff them directly with
// `git diff <tree1> <tree2>`, no checkout of either needed for the diff
// itself. Both treehashes and the result are then cached best-effort.
func (b *nativeOrchestrator) HasAllTargetsFileChange(ctx context.Context, first, second entity.BuildDescription) (retChanged bool, retErr error) {
	e := b.emitter.Tagged(map[string]string{metrics.TagRepo: url.ToShortRemote(first.Remote)})
	op := metrics.Begin(e, _opHasAllTargetsFileChange, metrics.SlowDurationBuckets)
	defer func() { op.Complete(retErr) }()

	repoCfg, ok := b.config.GetRepositoryConfig(first.Remote)
	if !ok || len(repoCfg.AllTargetsFiles) == 0 {
		return false, nil
	}
	logger := b.logger.With(zap.String("remote", first.Remote))

	treehash1, ok1 := b.readCachedTreehash(ctx, first)
	treehash2, ok2 := b.readCachedTreehash(ctx, second)
	if ok1 && ok2 {
		resultPath := cachekey.GetAllTargetsChangedCachePath(first.Remote, treehash1, treehash2, repoCfg.AllTargetsFiles)
		if changed, ok := b.readCachedBool(ctx, resultPath); ok {
			logger.Info("HasAllTargetsFileChange: Cache hit", zap.Bool("changed", changed))
			return changed, nil
		}
	}

	gitFactory := b.gitFactory
	if gitFactory == nil {
		gitFactory = func(dir string) git.Interface { return git.New(dir, b.logger) }
	}

	leaseStart := time.Now()
	ws, err := b.repoManager.Lease(ctx, first)
	recordStep(e, _opHasAllTargetsFileChange, "lease_duration", leaseStart, metrics.FastDurationBuckets)
	if err != nil {
		return false, classifyLeaseError(err)
	}
	defer func() {
		if err := ws.Release(); err != nil {
			if removeErr := os.RemoveAll(ws.Path()); removeErr != nil {
				logger.Error("HasAllTargetsFileChange: Failed to remove workspace", zap.Error(removeErr))
			}
		}
	}()
	gitModule := gitFactory(ws.Path())

	// A cached treehash for one or both revisions doesn't guarantee that
	// revision's tree object exists in *this* workspace's object database
	// (it may have been computed in a different worker, or long enough ago
	// that this worker never fetched it). Materialize both from scratch in
	// this single workspace so their objects are guaranteed to coexist for
	// the diff below.
	treehash1, err = b.materializeTreehash(ctx, e, ws, gitModule, first, logger)
	if err != nil {
		return false, err
	}
	treehash2, err = b.materializeTreehash(ctx, e, ws, gitModule, second, logger)
	if err != nil {
		return false, err
	}

	diffStart := time.Now()
	entries, err := gitModule.DiffWithStatus(ctx, treehash1, treehash2)
	recordStep(e, _opHasAllTargetsFileChange, "diff_duration", diffStart, metrics.FastDurationBuckets)
	if err != nil {
		return false, classifyGitError(fmt.Errorf("diff %s..%s: %w", treehash1, treehash2, err))
	}
	trigger := make(map[string]bool, len(repoCfg.AllTargetsFiles))
	for _, f := range repoCfg.AllTargetsFiles {
		trigger[f] = true
	}
	changed := false
	for _, entry := range entries {
		if trigger[entry.Path] {
			changed = true
			break
		}
	}

	b.cacheAllTargetsResult(logger, first, second, treehash1, treehash2, repoCfg.AllTargetsFiles, changed)
	logger.Info("HasAllTargetsFileChange: Computed", zap.Bool("changed", changed))
	return changed, nil
}

// readCachedTreehash returns the previously stored treehash for build, or
// ("", false) on a cache miss or read failure — either way the caller falls
// back to materializing the tree itself.
func (b *nativeOrchestrator) readCachedTreehash(ctx context.Context, build entity.BuildDescription) (string, bool) {
	resp, err := b.storage.Get(ctx, storage.DownloadRequest{Key: cachekey.GetTreehashCachePath(build)})
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.ReadCloser.Close() }()
	b64, err := io.ReadAll(resp.ReadCloser)
	if err != nil || len(b64) == 0 {
		return "", false
	}
	return string(b64), true
}

// readCachedBool returns the previously stored boolean at path, or (false,
// false) on a cache miss or read failure.
func (b *nativeOrchestrator) readCachedBool(ctx context.Context, path string) (bool, bool) {
	resp, err := b.storage.Get(ctx, storage.DownloadRequest{Key: path})
	if err != nil {
		return false, false
	}
	defer func() { _ = resp.ReadCloser.Close() }()
	raw, err := io.ReadAll(resp.ReadCloser)
	if err != nil || len(raw) == 0 {
		return false, false
	}
	return string(raw) == "true", true
}

// cacheAllTargetsResult best-effort writes both revisions' treehashes (so
// later GetTargetGraph/GetChangedTargets calls resolve them without
// recomputation) and this pair's all-targets-changed result. Writes use
// b.appCtx rather than the request context: HasAllTargetsFileChange has
// already paid for the workspace lease and checkout by this point, so
// letting a client disconnect abort just the final cache write (and force a
// full recompute next time) would waste that work for no benefit.
func (b *nativeOrchestrator) cacheAllTargetsResult(logger *zap.Logger, first, second entity.BuildDescription, treehash1, treehash2 string, allTargetsFiles []string, changed bool) {
	writes := map[string]string{
		cachekey.GetTreehashCachePath(first):                                                      treehash1,
		cachekey.GetTreehashCachePath(second):                                                      treehash2,
		cachekey.GetAllTargetsChangedCachePath(first.Remote, treehash1, treehash2, allTargetsFiles): fmt.Sprintf("%t", changed),
	}
	for path, value := range writes {
		if err := b.storage.Put(b.appCtx, storage.UploadRequest{Key: path, Reader: bytes.NewReader([]byte(value))}); err != nil {
			logger.Warn("HasAllTargetsFileChange: Failed to cache result", zap.String("path", path), zap.Error(err))
		}
	}
}

// readCachedGraph opens the cached graph for a treehash, preferring the TGB
// blob when the service is configured for it and falling back to the gob
// stream for entries written before the format flip. A TGB blob that exists
// but fails validation is treated as a miss (recompute overwrites it), not an
// infra failure. Returns a not-found error when neither format is present.
func (b *nativeOrchestrator) readCachedGraph(ctx context.Context, logger *zap.Logger, useTGB bool, tgbPath, gobPath string) (storage.GraphReader, error) {
	if useTGB {
		graphReader, err := storage.NewTGBGraphReader(ctx, b.storage, tgbPath, b.config.Service.MaxMessageBytes)
		if err == nil {
			return graphReader, nil
		}
		if errors.Is(err, storage.ErrCorruptTGB) {
			logger.Warn("GetTargetGraph: corrupt TGB blob, recomputing", zap.String("path", tgbPath), zap.Error(err))
		} else if !storage.IsNotFound(err) {
			return nil, err
		}
	}
	return storage.NewGraphReader(ctx, b.storage, gobPath)
}

// recordStep records a pipeline step's duration under the get_target_graph op.
func recordStep(e *metrics.Emitter, op, name string, start time.Time, buckets tally.DurationBuckets) {
	e.DurationHistogram(op, name, buckets).RecordDuration(time.Since(start))
}
