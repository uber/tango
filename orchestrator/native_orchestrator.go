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
	"github.com/uber/tango/core/common"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/repomanager"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/core/workspace"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/graphrunner"
	"github.com/uber/tango/internal/cachekey"
	"go.uber.org/zap"
)

// nativeOrchestrator implements native version of Orchestrator
type nativeOrchestrator struct {
	storage     storage.Storage
	repoManager repomanager.RepoManager
	logger      *zap.SugaredLogger
	scope       tally.Scope
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
	Logger      *zap.SugaredLogger
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

	return &nativeOrchestrator{
		storage:     p.Storage,
		repoManager: p.RepoManager,
		logger:      p.Logger,
		scope:       scope.SubScope("orchestrator"),
		gitFactory:  p.GitFactory,
		graphRunner: p.GraphRunner,
		appCtx:      appCtx,
		config:      p.Config,
	}, nil
}

// GetTargetGraph is used to compute the target graph locally.
// It leases a workspace, checks out the base revision, applies the change requests, and computes the target graph.
func (b *nativeOrchestrator) GetTargetGraph(ctx context.Context, req entity.GetTargetGraphRequest) (_ storage.GraphReader, retErr error) {
	scope := b.scope.SubScope("get_target_graph")
	scope.Counter("calls").Inc(1)
	defer func() {
		if retErr != nil {
			scope.Counter("failure").Inc(1)
			var ce common.ClassifiedError
			if !errors.As(retErr, &ce) {
				ce = common.WithReason(common.FailureReasonUnknown, common.ErrorTypeInfra, retErr)
			}
			scope.Tagged(map[string]string{
				"failure_type":   ce.Type(),
				"failure_reason": ce.Reason(),
			}).Counter("failure_type").Inc(1)
		} else {
			scope.Counter("success").Inc(1)
		}
	}()
	build := req.Build
	logger := b.logger.With(zap.Any("build_description", build))
	logger.Infow("GetTargetGraph: Processing request")

	remote := build.Remote
	repoCfg, ok := b.config.GetRepositoryConfig(remote)
	if !ok {
		return nil, fmt.Errorf("no repository configuration found for remote %q", remote)
	}
	ws, err := b.repoManager.Lease(ctx, build)
	if err != nil {
		return nil, fmt.Errorf("lease workspace: %w", err)
	}
	defer func() {
		err := ws.Release()
		if err != nil {
			// clean up the workspace if release fails.
			if removeErr := os.RemoveAll(ws.Path()); removeErr != nil {
				logger.Errorw("GetTargetGraph: Failed to remove workspace", zap.Error(removeErr))
			}
		}
	}()
	err = ws.Checkout(ctx, build.Remote, build.BaseSha)
	if err != nil {
		return nil, fmt.Errorf("checkout %s@%s: %w", build.Remote, build.BaseSha, err)
	}
	logger.Infow("GetTargetGraph: Checked out base revision")

	requests := make([]workspace.Request, 0, len(build.ChangeRequests))
	gitFactory := b.gitFactory
	if gitFactory == nil {
		gitFactory = func(dir string) git.Interface { return git.New(dir, b.logger) }
	}

	gitModule := gitFactory(ws.Path())
	for _, req := range build.ChangeRequests {
		request, err := workspace.NewRequest(req.URL, gitModule, build.BaseSha, req.Commit, logger)
		if err != nil {
			return nil, fmt.Errorf("create request for %q: %w", req.URL, err)
		}
		requests = append(requests, request)
	}
	err = ws.ApplyRequests(ctx, requests)
	if err != nil {
		return nil, fmt.Errorf("apply requests: %w", err)
	}
	logger.Infow("GetTargetGraph: Applied requests", zap.Int("request_count", len(requests)))

	// Compute the treehash and download the target graph from storage if exists.
	treehash, err := gitModule.RevParse(ctx, "HEAD^{tree}")
	if err != nil {
		return nil, fmt.Errorf("compute treehash: %w", err)
	}
	treehashPath := cachekey.GetGraphByTreeHash(build.Remote, treehash, build.Strategy, req.ExcludeFilesRegex)
	if !req.BypassCache {
		graphReader, err := storage.NewGraphReader(ctx, b.storage, treehashPath)
		if err == nil {
			logger.Infow("GetTargetGraph: Cache hit on treehash", zap.String("treehash", treehash))
			return graphReader, nil
		}
		if !storage.IsNotFound(err) {
			return nil, fmt.Errorf("read graph at treehash %s: %w", treehash, err)
		}
		logger.Infow("GetTargetGraph: Treehash not found, computing target graph", zap.String("treehash", treehash))
	} else {
		logger.Infow("GetTargetGraph: bypass_cache=true, computing target graph")
	}
	// Compute the target graph and store it in storage.
	runner := b.graphRunner
	if runner == nil {
		client, err := bazel.NewBazelClient(ctx, bazel.Params{
			WorkspacePath: ws.Path(),
			Logger:        b.logger,
			BazelCommand:  repoCfg.BazelCommand,
			QueryTimeout:  time.Duration(repoCfg.QueryTimeout) * time.Second,
			StreamLogs:    repoCfg.StreamBazelLogs,
		})
		if err != nil {
			return nil, fmt.Errorf("create bazel client: %w", err)
		}
		// Use default native graph runner
		runner = graphrunner.NewNativeGraphRunner(graphrunner.NativeGraphRunnerParams{
			BazelClient:        client,
			GitClient:          gitModule,
			Config:             repoCfg,
			ExtraExcludedFiles: req.ExcludeFilesRegex,
			Scope:              b.scope,
		})
	}
	result, err := runner.Compute(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("compute target graph: %w", err)
	}
	responses, err := common.ResultToGetTargetGraphResponse(ctx, result,
		b.config.Service.Chunking.TargetChunkSize,
		b.config.Service.Chunking.MetadataMapChunkSize,
	)
	if err != nil {
		return nil, fmt.Errorf("convert target graph to response: %w", err)
	}
	err = storage.WriteGraphStream(ctx, b.storage, treehashPath, responses)
	if err != nil {
		return nil, fmt.Errorf("write graph to storage at %s: %w", treehashPath, err)
	}
	treehashCachePath := cachekey.GetTreehashCachePath(build)
	treehashReader := bytes.NewReader([]byte(treehash))
	err = b.storage.Put(ctx, storage.UploadRequest{Key: treehashCachePath, Reader: treehashReader})
	if err != nil {
		return nil, fmt.Errorf("store treehash mapping at %s: %w", treehashCachePath, err)
	}
	graphReader, err := storage.NewGraphReader(ctx, b.storage, treehashPath)
	if err != nil {
		return nil, fmt.Errorf("create graph reader at %s: %w", treehashPath, err)
	}
	logger.Infow("GetTargetGraph: Done computing and storing target graph", zap.String("treehash", treehash))
	return graphReader, nil
}
