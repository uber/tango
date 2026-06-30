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
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// GraphRunnerFactory creates a GraphRunner for a materialized workspace.
// Set at construction time so the request path has no nil-check branching.
type GraphRunnerFactory func(ctx context.Context, ws workspace.Workspace, gitClient git.Interface, repoCfg config.RepositoryConfig, extraExcludedFiles []string) (graphrunner.GraphRunner, error)

// nativeOrchestrator implements native version of Orchestrator
type nativeOrchestrator struct {
	storage            storage.Storage
	repoManager        repomanager.RepoManager
	logger             *zap.SugaredLogger
	gitFactory         func(directory string) git.Interface
	graphRunnerFactory GraphRunnerFactory
	config             config.RepositoryConfigProvider

	// metrics are pre-created at construction time.
	getTargetGraphScope   tally.Scope
	getTargetGraphCalls   tally.Counter
	getTargetGraphSuccess tally.Counter
	getTargetGraphFailure tally.Counter
}

type Params struct {
	fx.In

	Storage            storage.Storage
	RepoManager        repomanager.RepoManager
	Logger             *zap.SugaredLogger
	Scope              tally.Scope                          `optional:"true"`
	GitFactory         func(directory string) git.Interface `optional:"true"`
	GraphRunnerFactory GraphRunnerFactory                   `optional:"true"`
	Config             config.RepositoryConfigProvider
}

// NewNativeOrchestrator creates a new native orchestrator with the given parameters.
func NewNativeOrchestrator(p Params) Orchestrator {
	scope := p.Scope
	if scope == nil {
		scope = tally.NoopScope
	}
	gitFactory := p.GitFactory
	if gitFactory == nil {
		gitFactory = git.New
	}
	orchScope := scope.SubScope("orchestrator")
	grFactory := p.GraphRunnerFactory
	if grFactory == nil {
		grFactory = defaultGraphRunnerFactory(p.Logger, orchScope)
	}
	gtgScope := orchScope.SubScope("get_target_graph")
	return &nativeOrchestrator{
		storage:               p.Storage,
		repoManager:           p.RepoManager,
		logger:                p.Logger,
		gitFactory:            gitFactory,
		graphRunnerFactory:    grFactory,
		config:                p.Config,
		getTargetGraphScope:   gtgScope,
		getTargetGraphCalls:   gtgScope.Counter("calls"),
		getTargetGraphSuccess: gtgScope.Counter("success"),
		getTargetGraphFailure: gtgScope.Counter("failure"),
	}
}

// defaultGraphRunnerFactory returns a factory that creates a NativeGraphRunner
// backed by a bazel client for the given workspace.
func defaultGraphRunnerFactory(logger *zap.SugaredLogger, scope tally.Scope) GraphRunnerFactory {
	return func(ctx context.Context, ws workspace.Workspace, gitClient git.Interface, repoCfg config.RepositoryConfig, extraExcludedFiles []string) (graphrunner.GraphRunner, error) {
		client, err := bazel.NewBazelClient(ctx, bazel.Params{
			WorkspacePath: ws.Path(),
			Logger:        logger,
			BazelCommand:  repoCfg.BazelCommand,
			QueryTimeout:  time.Duration(repoCfg.QueryTimeout) * time.Second,
			StreamLogs:    repoCfg.StreamBazelLogs,
		})
		if err != nil {
			return nil, fmt.Errorf("create bazel client: %w", err)
		}
		return graphrunner.NewNativeGraphRunner(graphrunner.NativeGraphRunnerParams{
			BazelClient:        client,
			GitClient:          gitClient,
			Config:             repoCfg,
			ExtraExcludedFiles: extraExcludedFiles,
			Scope:              scope,
		}), nil
	}
}

// preparedWorkspace holds the result of leasing and preparing a workspace.
type preparedWorkspace struct {
	ws      workspace.Workspace
	git     git.Interface
	repoCfg config.RepositoryConfig
}

// prepareWorkspace leases a workspace, checks out the base revision, and applies
// change requests. The returned cleanup function releases the workspace and must
// be deferred by the caller.
func (b *nativeOrchestrator) prepareWorkspace(ctx context.Context, desc *pb.BuildDescription, logger *zap.SugaredLogger) (_ *preparedWorkspace, cleanup func(), retErr error) {
	repoCfg, ok := b.config.GetRepositoryConfig(desc.Remote)
	if !ok {
		return nil, nil, fmt.Errorf("no repository configuration found for remote %q", desc.Remote)
	}
	ws, err := b.repoManager.Lease(ctx, *desc)
	if err != nil {
		return nil, nil, fmt.Errorf("lease workspace: %w", err)
	}
	cleanup = func() {
		if err := ws.Release(); err != nil {
			if removeErr := os.RemoveAll(ws.Path()); removeErr != nil {
				logger.Errorw("GetTargetGraph: Failed to remove workspace", zap.Error(removeErr))
			}
		}
	}
	defer func() {
		if retErr != nil {
			cleanup()
		}
	}()
	if err := ws.Checkout(ctx, desc.Remote, desc.BaseSha); err != nil {
		return nil, nil, fmt.Errorf("checkout %s@%s: %w", desc.Remote, desc.BaseSha, err)
	}
	logger.Infow("GetTargetGraph: Checked out base revision")

	gitModule := b.gitFactory(ws.Path())
	requests := make([]workspace.Request, 0, len(desc.Requests))
	for _, req := range desc.Requests {
		r, err := workspace.NewRequest(req.GetUrl(), gitModule, desc.BaseSha, req.GetCommit(), logger)
		if err != nil {
			return nil, nil, fmt.Errorf("create request for %q: %w", req.GetUrl(), err)
		}
		requests = append(requests, r)
	}
	if err := ws.ApplyRequests(ctx, requests); err != nil {
		return nil, nil, fmt.Errorf("apply requests: %w", err)
	}
	logger.Infow("GetTargetGraph: Applied requests", zap.Int("request_count", len(requests)))

	return &preparedWorkspace{ws: ws, git: gitModule, repoCfg: repoCfg}, cleanup, nil
}

// computeGraph computes (or fetches from cache) the target graph for a prepared
// workspace and stores the result.
func (b *nativeOrchestrator) computeGraph(ctx context.Context, pw *preparedWorkspace, param GetTargetGraphParam, logger *zap.SugaredLogger) (storage.GraphReader, error) {
	desc := param.Req.BuildDescription
	treehash, err := pw.git.RevParse(ctx, "HEAD^{tree}")
	if err != nil {
		return nil, fmt.Errorf("compute treehash: %w", err)
	}
	treehashPath := common.GetGraphByTreeHash(desc.Remote, treehash, desc.GetStrategy(), param.Req.GetRequestOptions())

	if !param.BypassCache {
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

	runner, err := b.graphRunnerFactory(ctx, pw.ws, pw.git, pw.repoCfg, param.Req.GetRequestOptions().GetExtraExcludeFilesRegex())
	if err != nil {
		return nil, err
	}
	result, err := runner.Compute(ctx, pw.ws)
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
	treehashCachePath := common.GetTreehashCachePath(desc)
	if err := b.storage.Put(ctx, storage.UploadRequest{Key: treehashCachePath, Reader: bytes.NewReader([]byte(treehash))}); err != nil {
		return nil, fmt.Errorf("store treehash mapping at %s: %w", treehashCachePath, err)
	}
	graphReader, err := storage.NewGraphReader(ctx, b.storage, treehashPath)
	if err != nil {
		return nil, fmt.Errorf("create graph reader at %s: %w", treehashPath, err)
	}
	logger.Infow("GetTargetGraph: Done computing and storing target graph", zap.String("treehash", treehash))
	return graphReader, nil
}

// GetTargetGraph computes the target graph locally.
func (b *nativeOrchestrator) GetTargetGraph(ctx context.Context, param GetTargetGraphParam) (_ storage.GraphReader, retErr error) {
	b.getTargetGraphCalls.Inc(1)
	defer func() {
		if retErr != nil {
			b.getTargetGraphFailure.Inc(1)
			var ce common.ClassifiedError
			if !errors.As(retErr, &ce) {
				ce = common.WithReason(common.FailureReasonUnknown, common.ErrorTypeInfra, retErr)
			}
			b.getTargetGraphScope.Tagged(map[string]string{
				"failure_type":   ce.Type(),
				"failure_reason": ce.Reason(),
			}).Counter("failure_type").Inc(1)
		} else {
			b.getTargetGraphSuccess.Inc(1)
		}
	}()

	logger := b.logger.With(zap.Any("build_description", param.Req.BuildDescription))
	logger.Infow("GetTargetGraph: Processing request")

	pw, cleanup, err := b.prepareWorkspace(ctx, param.Req.BuildDescription, logger)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	return b.computeGraph(ctx, pw, param, logger)
}
