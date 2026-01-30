package orchestrator

import (
	"bytes"
	"context"
	"os"

	"github.com/uber/tango/core/bazel"
	"github.com/uber/tango/core/bazelrunner"
	"github.com/uber/tango/core/common"
	"github.com/uber/tango/core/config"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/repomanager"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/core/workspace"
	"go.uber.org/zap"
	"time"
)

// nativeOrchestrator implements native version of Orchestrator
type nativeOrchestrator struct {
	storage     storage.Storage
	repoManager repomanager.RepoManager
	logger      *zap.SugaredLogger
	// gitFactory allows injecting a git.Interface constructor for testing
	gitFactory     func(directory string) git.Interface
	graphRunner    bazelrunner.GraphRunner
	configFilePath string
}

type Params struct {
	Storage        storage.Storage
	RepoManager    repomanager.RepoManager
	Logger         *zap.SugaredLogger
	GitFactory     func(directory string) git.Interface
	GraphRunner    bazelrunner.GraphRunner
	ConfigFilePath string
}

// NewNativeOrchestrator creates a new native orchestrator with the given parameters.
func NewNativeOrchestrator(p Params) Orchestrator {
	return &nativeOrchestrator{
		storage:        p.Storage,
		repoManager:    p.RepoManager,
		logger:         p.Logger,
		gitFactory:     p.GitFactory,
		graphRunner:    p.GraphRunner,
		configFilePath: p.ConfigFilePath,
	}
}

// GetTargetGraph is used to compute the target graph locally.
// It leases a workspace, checks out the base revision, applies the change requests, and computes the target graph.
func (b *nativeOrchestrator) GetTargetGraph(ctx context.Context, param GetTargetGraphParam) (storage.GraphReader, error) {
	// parse the config file
	config, err := config.ParseConfig(b.configFilePath)
	if err != nil {
		b.logger.Error("getGraph: Error parsing config file", zap.String("configFilePath", b.configFilePath), zap.Error(err))
		return nil, err
	}
	ws, err := b.repoManager.Lease(ctx, *param.Req.BuildDescription)
	if err != nil {
		return nil, err
	}
	defer func() {
		err := ws.Release()
		if err != nil {
			// clean up the workspace if release fails.
			if removeErr := os.RemoveAll(ws.Path()); removeErr != nil {
				b.logger.Errorf("failed to remove workspace: %v", removeErr)
			}
		}
	}()
	err = ws.Checkout(ctx, param.Req.BuildDescription.Remote, param.Req.BuildDescription.BaseSha)
	if err != nil {
		return nil, err
	}

	requests := make([]workspace.Request, 0, len(param.Req.BuildDescription.RequestUrls))
	factory := b.gitFactory
	if factory == nil {
		factory = git.New
	}

	gitModule := factory(ws.Path())
	for _, url := range param.Req.BuildDescription.RequestUrls {
		request, err := workspace.NewRequest(url, gitModule, param.Req.BuildDescription.BaseSha)
		if err != nil {
			b.logger.Error("getGraph: Error creating request", zap.Any("request build description", param.Req.BuildDescription), zap.Error(err))
			return nil, err
		}
		requests = append(requests, request)
	}
	err = ws.ApplyRequests(ctx, requests)
	if err != nil {
		b.logger.Error("getGraph: Error applying requests to workspace", zap.Any("request build description", param.Req.BuildDescription), zap.Error(err))
		return nil, err
	}
	// Compute the treehash and download the target graph from storage if exists.
	treehash, err := gitModule.RevParse(ctx, "HEAD^{tree}")
	if err != nil {
		b.logger.Error("Treehash computation failed", zap.Any("request build description", param.Req.BuildDescription), zap.Error(err))
		return nil, err
	}
	treehashPath := common.GetGraphByTreeHash(param.Req.BuildDescription.Remote, treehash)
	graphReader, err := storage.NewGraphReader(ctx, b.storage, treehashPath)
	if err == nil {
		return graphReader, nil
	}
	if err != nil {
		if storage.IsNotFound(err) {
			b.logger.Info("getGraph: treehash not found. Computing the target graph.", zap.Any("request build description", param.Req.BuildDescription), zap.Error(err))
			// Compute the target graph and store it in storage.
			runner := b.graphRunner
			if runner == nil {
				client, err := bazel.NewBazelClient(bazel.Params{
					WorkspacePath: ws.Path(),
					Logger:        b.logger,
					BazelCommand:  config.BazelCommand,
					QueryTimeout:  time.Duration(config.QueryTimeout) * time.Second,
				})
				if err != nil {
					b.logger.Error("getGraph: Error creating bazel client", zap.Error(err))
					return nil, err
				}
				// Use default native graph runner
				runner = bazelrunner.NewNativeGraphRunner(bazelrunner.NativeGraphRunnerParams{
					BazelClient: client,
					GitClient:   gitModule,
					Config:      *config,
				})
			}
			result, err := runner.Compute(ctx, ws)
			if err != nil {
				b.logger.Error("getGraph: Error computing target graph", zap.Any("request build description", param.Req.BuildDescription), zap.Error(err))
				return nil, err
			}
			graphs, err := common.ResultToGetTargetGraphResponse(result)
			if err != nil {
				b.logger.Error("getGraph: Error converting target graph to GetTargetGraphResponse", zap.Any("request build description", param.Req.BuildDescription), zap.Error(err))
				return nil, err
			}
			err = storage.WriteGraphStream(ctx, b.storage, treehashPath, graphs)
			if err != nil {
				b.logger.Error("getGraph: Error writing target graph to storage", zap.Any("request build description", param.Req.BuildDescription), zap.Error(err))
				return nil, err
			}
		} else {
			// Other errors (network, infra issues) should be retried
			b.logger.Error("getGraph: Storage error", zap.Any("request build description", param.Req.BuildDescription), zap.Error(err))
			return nil, err
		}
	}
	// Map build description to treehash for future lookup.
	treehashCachePath := common.GetTreehashCachePath(param.Req.BuildDescription)
	treehashReader := bytes.NewReader([]byte(treehash))
	err = b.storage.Put(ctx, storage.UploadRequest{Key: treehashCachePath, Reader: treehashReader})
	if err != nil {
		b.logger.Error("getGraph: Error reading target graph from storage", zap.Any("request build description", param.Req.BuildDescription), zap.Error(err))
		return nil, err
	}
	graphReader, err = storage.NewGraphReader(ctx, b.storage, treehashPath)
	if err != nil {
		b.logger.Error("getGraph: Error creating graph reader", zap.Any("request build description", param.Req.BuildDescription), zap.Error(err))
		return nil, err
	}
	return graphReader, nil
}
