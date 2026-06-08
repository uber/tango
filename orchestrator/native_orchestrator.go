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
	// gitFactory allows injecting a git.Interface constructor for testing
	gitFactory     func(directory string) git.Interface
	graphRunner    graphrunner.GraphRunner
	configFilePath string
}

type Params struct {
	Storage        storage.Storage
	RepoManager    repomanager.RepoManager
	Logger         *zap.SugaredLogger
	Scope          tally.Scope
	GitFactory     func(directory string) git.Interface
	GraphRunner    graphrunner.GraphRunner
	ConfigFilePath string
}

// NewNativeOrchestrator creates a new native orchestrator with the given parameters.
func NewNativeOrchestrator(p Params) Orchestrator {
	scope := p.Scope
	if scope == nil {
		scope = tally.NoopScope
	}
	return &nativeOrchestrator{
		storage:        p.Storage,
		repoManager:    p.RepoManager,
		logger:         p.Logger,
		scope:          scope.SubScope("orchestrator"),
		gitFactory:     p.GitFactory,
		graphRunner:    p.GraphRunner,
		configFilePath: p.ConfigFilePath,
	}
}

// GetTargetGraph is used to compute the target graph locally.
// It leases a workspace, checks out the base revision, applies the change requests, and computes the target graph.
func (b *nativeOrchestrator) GetTargetGraph(ctx context.Context, param GetTargetGraphParam) (_ storage.GraphReader, retErr error) {
	scope := b.scope.SubScope("get_target_graph")
	scope.Counter("calls").Inc(1)
	defer func() {
		if retErr != nil {
			scope.Counter("failure").Inc(1)
			var ce common.ClassifiedError
			if !errors.As(retErr, &ce) {
				ce = common.WithReason(failureReasonUnknown, common.ErrorTypeInfra, retErr)
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

	// parse the config file
	cfg, err := config.Parse(b.configFilePath)
	if err != nil {
		logger.Errorw("GetTargetGraph: Error parsing config file", zap.String("configFilePath", b.configFilePath), zap.Error(err))
		return nil, common.WithReason(failureReasonConfigParse, common.ErrorTypeInfra, err)
	}
	remote := param.Req.BuildDescription.Remote
	repoCfg, ok := cfg.GetRepositoryConfig(remote)
	if !ok {
		return nil, common.WithReason(failureReasonNoRepoConfig, common.ErrorTypeUser, fmt.Errorf("no repository configuration found for remote %q", remote))
	}
	ws, err := b.repoManager.Lease(ctx, *param.Req.BuildDescription)
	if err != nil {
		logger.Errorw("GetTargetGraph: Error leasing workspace", zap.Error(err))
		return nil, common.WithReason(failureReasonWorkspaceLease, common.ErrorTypeInfra, err)
	}
	defer func() {
		err := ws.Release()
		if err != nil {
			// clean up the workspace if release fails.
			if removeErr := os.RemoveAll(ws.Path()); removeErr != nil {
				logger.Errorf("GetTargetGraph: Failed to remove workspace: %v", removeErr)
			}
		}
	}()
	err = ws.Checkout(ctx, param.Req.BuildDescription.Remote, param.Req.BuildDescription.BaseSha)
	if err != nil {
		logger.Errorw("GetTargetGraph: Error checking out base revision", zap.Error(err))
		return nil, common.WithReason(failureReasonWorkspaceCheckout, common.ErrorTypeInfra, err)
	}
	logger.Infow("GetTargetGraph: Checked out base revision")

	requests := make([]workspace.Request, 0, len(param.Req.BuildDescription.Requests))
	factory := b.gitFactory
	if factory == nil {
		factory = git.New
	}

	gitModule := factory(ws.Path())
	for _, req := range param.Req.BuildDescription.Requests {
		request, err := workspace.NewRequest(req.GetUrl(), gitModule, param.Req.BuildDescription.BaseSha, req.GetCommit(), logger)
		if err != nil {
			logger.Errorw("GetTargetGraph: Error creating request", zap.String("url", req.GetUrl()), zap.Error(err))
			return nil, common.WithReason(failureReasonRequestCreate, common.ErrorTypeInfra, err)
		}
		requests = append(requests, request)
	}
	err = ws.ApplyRequests(ctx, requests)
	if err != nil {
		logger.Errorw("GetTargetGraph: Error applying requests to workspace", zap.Error(err))
		return nil, common.WithReason(failureReasonRequestApply, common.ErrorTypeInfra, err)
	}
	logger.Infow("GetTargetGraph: Applied requests", zap.Int("request_count", len(requests)))

	// Compute the treehash and download the target graph from storage if exists.
	treehash, err := gitModule.RevParse(ctx, "HEAD^{tree}")
	if err != nil {
		logger.Errorw("GetTargetGraph: Treehash computation failed", zap.Error(err))
		return nil, common.WithReason(failureReasonTreehashCompute, common.ErrorTypeInfra, err)
	}
	treehashPath := common.GetGraphByTreeHash(param.Req.BuildDescription.Remote, treehash, param.Req.BuildDescription.GetStrategy(), param.Req.GetRequestOptions())
	if !param.BypassCache {
		graphReader, err := storage.NewGraphReader(ctx, b.storage, treehashPath)
		if err == nil {
			logger.Infow("GetTargetGraph: Cache hit on treehash", zap.String("treehash", treehash))
			return graphReader, nil
		}
		if !storage.IsNotFound(err) {
			logger.Errorw("GetTargetGraph: Storage error", zap.Error(err))
			return nil, common.WithReason(failureReasonStorage, common.ErrorTypeInfra, err)
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
		})
		if err != nil {
			logger.Errorw("GetTargetGraph: Error creating bazel client", zap.Error(err))
			return nil, common.WithReason(failureReasonBazelClient, common.ErrorTypeInfra, err)
		}
		// Use default native graph runner
		runner = graphrunner.NewNativeGraphRunner(graphrunner.NativeGraphRunnerParams{
			BazelClient:        client,
			GitClient:          gitModule,
			Config:             repoCfg,
			ExtraExcludedFiles: param.Req.GetRequestOptions().GetExtraExcludeFilesRegex(),
			Scope:              b.scope,
		})
	}
	result, err := runner.Compute(ctx, ws)
	if err != nil {
		logger.Errorw("GetTargetGraph: Error computing target graph", zap.Error(err))
		return nil, common.WithReason(failureReasonGraphCompute, common.ErrorTypeInfra, err)
	}
	responses, err := common.ResultToGetTargetGraphResponse(ctx, result)
	if err != nil {
		logger.Errorw("GetTargetGraph: Error converting target graph to response", zap.Error(err))
		return nil, common.WithReason(failureReasonGraphConvert, common.ErrorTypeInfra, err)
	}
	err = storage.WriteGraphStream(ctx, b.storage, treehashPath, responses)
	if err != nil {
		logger.Errorw("GetTargetGraph: Error writing target graph to storage", zap.Error(err))
		return nil, common.WithReason(failureReasonStorage, common.ErrorTypeInfra, err)
	}
	treehashCachePath := common.GetTreehashCachePath(param.Req.BuildDescription)
	treehashReader := bytes.NewReader([]byte(treehash))
	err = b.storage.Put(ctx, storage.UploadRequest{Key: treehashCachePath, Reader: treehashReader})
	if err != nil {
		logger.Errorw("GetTargetGraph: Error storing treehash mapping", zap.Error(err))
		return nil, common.WithReason(failureReasonStorage, common.ErrorTypeInfra, err)
	}
	graphReader, err := storage.NewGraphReader(ctx, b.storage, treehashPath)
	if err != nil {
		logger.Errorw("GetTargetGraph: Error creating graph reader", zap.Error(err))
		return nil, common.WithReason(failureReasonStorage, common.ErrorTypeInfra, err)
	}
	logger.Infow("GetTargetGraph: Done computing and storing target graph", zap.String("treehash", treehash))
	return graphReader, nil
}
