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
	"github.com/uber/tango/graphrunner"
	"go.uber.org/zap"
)

// nativeOrchestrator implements native version of Orchestrator
type nativeOrchestrator struct {
	storage     storage.Storage
	repoManager repomanager.RepoManager
	logger      *zap.SugaredLogger
	scope       tally.Scope
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
	// GitFactory allows injecting a git.Interface constructor for testing.
	// Defaults to git.New if nil.
	GitFactory  func(directory string) git.Interface
	GraphRunner graphrunner.GraphRunner
	Config      *config.Config
}

// NewNativeOrchestrator creates a new native orchestrator with the given parameters.
//
// appCtx is the application-lifetime context. Cancel it when the process is
// shutting down (e.g. wire it to SIGTERM/SIGINT in main) to abort any
// background goroutines the orchestrator spawns.
func NewNativeOrchestrator(appCtx context.Context, p Params) Orchestrator {
	scope := p.Scope
	if scope == nil {
		scope = tally.NoopScope
	}
	gitFactory := p.GitFactory
	if gitFactory == nil {
		gitFactory = git.New
	}
	return &nativeOrchestrator{
		storage:     p.Storage,
		repoManager: p.RepoManager,
		logger:      p.Logger,
		scope:       scope.SubScope("orchestrator"),
		gitFactory:  gitFactory,
		graphRunner: p.GraphRunner,
		config:      p.Config,
		appCtx:      appCtx,
	}
}

type preparedWorkspace struct {
	ws       workspace.Workspace
	treehash string
	repoCfg  config.RepositoryConfig
}

// prepareWorkspace leases a workspace, checks out the base revision, applies
// change requests, and computes the treehash. The returned cleanup function
// must be deferred by the caller to release the workspace.
func (b *nativeOrchestrator) prepareWorkspace(ctx context.Context, param GetTargetGraphParam) (_ preparedWorkspace, cleanup func(), retErr error) {
	logger := b.logger.With(zap.Any("build_description", param.Req.BuildDescription))
	desc := param.Req.BuildDescription

	remote := desc.Remote
	repoCfg, ok := b.config.GetRepositoryConfig(remote)
	if !ok {
		return preparedWorkspace{}, nil, fmt.Errorf("no repository configuration found for remote %q", remote)
	}

	ws, err := b.repoManager.Lease(ctx, *desc)
	if err != nil {
		return preparedWorkspace{}, nil, fmt.Errorf("lease workspace: %w", err)
	}
	releaseWorkspace := func() {
		if err := ws.Release(); err != nil {
			if removeErr := os.RemoveAll(ws.Path()); removeErr != nil {
				logger.Errorw("prepareWorkspace: Failed to remove workspace", zap.Error(removeErr))
			}
		}
	}
	defer func() {
		if retErr != nil {
			releaseWorkspace()
		}
	}()

	if err := ws.Checkout(ctx, remote, desc.BaseSha); err != nil {
		return preparedWorkspace{}, nil, fmt.Errorf("checkout %s@%s: %w", remote, desc.BaseSha, err)
	}
	logger.Infow("prepareWorkspace: Checked out base revision")

	gitModule := b.gitFactory(ws.Path())
	requests := make([]workspace.Request, 0, len(desc.Requests))
	for _, req := range desc.Requests {
		request, err := workspace.NewRequest(req.GetUrl(), gitModule, desc.BaseSha, req.GetCommit(), logger)
		if err != nil {
			return preparedWorkspace{}, nil, fmt.Errorf("create request for %q: %w", req.GetUrl(), err)
		}
		requests = append(requests, request)
	}
	if err := ws.ApplyRequests(ctx, requests); err != nil {
		return preparedWorkspace{}, nil, fmt.Errorf("apply requests: %w", err)
	}
	logger.Infow("prepareWorkspace: Applied requests", zap.Int("request_count", len(requests)))

	treehash, err := gitModule.RevParse(ctx, "HEAD^{tree}")
	if err != nil {
		return preparedWorkspace{}, nil, fmt.Errorf("compute treehash: %w", err)
	}

	return preparedWorkspace{ws: ws, treehash: treehash, repoCfg: repoCfg}, releaseWorkspace, nil
}

// GetTargetGraph computes the target graph locally.
// It prepares a workspace (lease, checkout, apply requests), checks the cache,
// and falls through to graph computation on a cache miss.
func (b *nativeOrchestrator) GetTargetGraph(ctx context.Context, param GetTargetGraphParam) (_ storage.GraphReader, retErr error) {
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
	logger := b.logger.With(zap.Any("build_description", param.Req.BuildDescription))
	logger.Infow("GetTargetGraph: Processing request")

	prepared, cleanup, err := b.prepareWorkspace(ctx, param)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	treehashPath := common.GetGraphByTreeHash(param.Req.BuildDescription.Remote, prepared.treehash, param.Req.BuildDescription.GetStrategy(), param.Req.GetRequestOptions())
	if !param.BypassCache {
		graphReader, err := storage.NewGraphReader(ctx, b.storage, treehashPath)
		if err == nil {
			logger.Infow("GetTargetGraph: Cache hit on treehash", zap.String("treehash", prepared.treehash))
			return graphReader, nil
		}
		if !storage.IsNotFound(err) {
			return nil, fmt.Errorf("read graph at treehash %s: %w", prepared.treehash, err)
		}
		logger.Infow("GetTargetGraph: Treehash not found, computing target graph", zap.String("treehash", prepared.treehash))
	} else {
		logger.Infow("GetTargetGraph: bypass_cache=true, computing target graph")
	}

	runner := b.graphRunner
	if runner == nil {
		client, err := bazel.NewBazelClient(ctx, bazel.Params{
			WorkspacePath: prepared.ws.Path(),
			Logger:        b.logger,
			BazelCommand:  prepared.repoCfg.BazelCommand,
			QueryTimeout:  time.Duration(prepared.repoCfg.QueryTimeout) * time.Second,
			StreamLogs:    prepared.repoCfg.StreamBazelLogs,
		})
		if err != nil {
			return nil, fmt.Errorf("create bazel client: %w", err)
		}
		runner = graphrunner.NewNativeGraphRunner(graphrunner.NativeGraphRunnerParams{
			BazelClient:        client,
			GitClient:          b.gitFactory(prepared.ws.Path()),
			Config:             prepared.repoCfg,
			ExtraExcludedFiles: param.Req.GetRequestOptions().GetExtraExcludeFilesRegex(),
			Scope:              b.scope,
		})
	}
	result, err := runner.Compute(ctx, prepared.ws)
	if err != nil {
		return nil, fmt.Errorf("compute target graph: %w", err)
	}
	responses, err := common.ResultToGetTargetGraphResponse(ctx, result)
	if err != nil {
		return nil, fmt.Errorf("convert target graph to response: %w", err)
	}
	if err := storage.WriteGraphStream(ctx, b.storage, treehashPath, responses); err != nil {
		return nil, fmt.Errorf("write graph to storage at %s: %w", treehashPath, err)
	}
	treehashCachePath := common.GetTreehashCachePath(param.Req.BuildDescription)
	treehashReader := bytes.NewReader([]byte(prepared.treehash))
	if err := b.storage.Put(ctx, storage.UploadRequest{Key: treehashCachePath, Reader: treehashReader}); err != nil {
		return nil, fmt.Errorf("store treehash mapping at %s: %w", treehashCachePath, err)
	}
	graphReader, err := storage.NewGraphReader(ctx, b.storage, treehashPath)
	if err != nil {
		return nil, fmt.Errorf("create graph reader at %s: %w", treehashPath, err)
	}
	logger.Infow("GetTargetGraph: Done computing and storing target graph", zap.String("treehash", prepared.treehash))
	return graphReader, nil
}
