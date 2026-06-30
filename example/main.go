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

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/uber/tango/config"
	"github.com/uber/tango/controller"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/repomanager"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/core/storage/disk"
	"github.com/uber/tango/orchestrator"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/fx"
	"go.uber.org/yarpc"
	"go.uber.org/yarpc/api/transport"
	yarpcgrpc "go.uber.org/yarpc/transport/grpc"
	"go.uber.org/zap"
)

func main() {
	fx.New(
		fx.Provide(
			newLogger,
			newConfig,
			newStorage,
			newAppContext,
			newRepoManager,
			newOrchestrator,
			newController,
			newDispatcher,
		),
		fx.Invoke(registerWorkspaceDirs, startDispatcher),
	).Run()
}

func newLogger() (*zap.Logger, *zap.SugaredLogger) {
	zl, _ := zap.NewDevelopment()
	return zl, zl.Sugar()
}

func newConfig() (*config.Config, error) {
	return config.Parse(filepath.Join("example", "tango-config.yaml"))
}

func newStorage(cfg *config.Config) (storage.Storage, error) {
	sc := cfg.Storage
	switch sc.Type {
	case config.StorageTypeMemory, "":
		return storage.NewMemoryStorage(), nil
	case config.StorageTypeDisk:
		if sc.Disk == nil {
			return nil, fmt.Errorf("disk storage requires 'disk' configuration")
		}
		if sc.Disk.RootPath == "" {
			return nil, fmt.Errorf("disk storage requires 'root_path' to be set")
		}
		return disk.New(sc.Disk.RootPath)
	default:
		return nil, fmt.Errorf("unsupported storage type: %q", sc.Type)
	}
}

// appContext wraps a context.Context so Fx can distinguish it from other
// context values in the dependency graph.
type appContext struct {
	context.Context
}

// newAppContext creates an application-lifetime context that is cancelled
// when Fx stops the application (SIGINT/SIGTERM).
func newAppContext(lc fx.Lifecycle) appContext {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStop: func(context.Context) error {
			cancel()
			return nil
		},
	})
	return appContext{ctx}
}

func registerWorkspaceDirs(lc fx.Lifecycle, cfg *config.Config) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			if err := os.MkdirAll(cfg.Service.RepoManagerClonePath, 0o755); err != nil {
				return fmt.Errorf("create repo manager clone path: %w", err)
			}
			return os.MkdirAll(cfg.Service.WorkerRootPath, 0o755)
		},
		OnStop: func(context.Context) error {
			os.RemoveAll(cfg.Service.WorkerRootPath)
			os.RemoveAll(cfg.Service.RepoManagerClonePath)
			return nil
		},
	})
}

func newRepoManager(appCtx appContext, cfg *config.Config, logger *zap.SugaredLogger) repomanager.RepoManager {
	return repomanager.NewRepoManager(appCtx, repomanager.Params{
		Git:                  git.New(cfg.Service.RepoManagerClonePath),
		Logger:               logger,
		RepoManagerClonePath: cfg.Service.RepoManagerClonePath,
		WorkerRootPath:       cfg.Service.WorkerRootPath,
		PoolSize:             cfg.Service.WorkerPoolSize,
	})
}

func newOrchestrator(appCtx appContext, cfg *config.Config, store storage.Storage, rm repomanager.RepoManager, logger *zap.SugaredLogger) orchestrator.Orchestrator {
	return orchestrator.NewNativeOrchestrator(appCtx, orchestrator.Params{
		Storage:     store,
		RepoManager: rm,
		Logger:      logger,
		GitFactory:  git.New,
		Config:      cfg,
	})
}

func newController(appCtx appContext, zl *zap.Logger, store storage.Storage, orch orchestrator.Orchestrator) pb.TangoYARPCServer {
	return controller.NewController(appCtx, controller.Params{
		Logger:       zl,
		Storage:      store,
		Orchestrator: orch,
	})
}

func newDispatcher(ctrl pb.TangoYARPCServer) (*yarpc.Dispatcher, error) {
	grpcTransport := yarpcgrpc.NewTransport()
	port := "127.0.0.1:8081"
	grpcListener, err := net.Listen("tcp", port)
	if err != nil {
		return nil, fmt.Errorf("listen on gRPC port: %w", err)
	}
	inbounds := []transport.Inbound{
		grpcTransport.NewInbound(grpcListener),
	}
	dispatcher := yarpc.NewDispatcher(yarpc.Config{
		Name:     "tango",
		Inbounds: inbounds,
	})
	dispatcher.Register(pb.BuildTangoYARPCProcedures(ctrl))
	return dispatcher, nil
}

func startDispatcher(lc fx.Lifecycle, d *yarpc.Dispatcher, logger *zap.SugaredLogger) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			logger.Infof("Tango server starting on 127.0.0.1:8081")
			return d.Start()
		},
		OnStop: func(context.Context) error {
			return d.Stop()
		},
	})
}
