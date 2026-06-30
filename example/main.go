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
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/core/storage/disk"
	tangomodule "github.com/uber/tango/module"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/fx"
	"go.uber.org/yarpc"
	"go.uber.org/yarpc/api/transport"
	yarpcgrpc "go.uber.org/yarpc/transport/grpc"
	"go.uber.org/zap"
)

func main() {
	fx.New(
		tangomodule.Module,
		fx.Provide(
			provideConfig,
			provideStorage,
			provideLoggers,
			provideGit,
			provideAppCtx,
		),
		fx.Invoke(startServer),
	).Run()
}

func provideConfig() (*config.Config, config.RepositoryConfigProvider, error) {
	configFilePath := filepath.Join("example", "tango-config.yaml")
	cfg, err := config.Parse(configFilePath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse config: %w", err)
	}
	return cfg, cfg, nil
}

func provideStorage(cfg *config.Config) (storage.Storage, error) {
	return newStorage(cfg.Storage)
}

type loggerResult struct {
	fx.Out
	Logger        *zap.Logger
	SugaredLogger *zap.SugaredLogger
}

func provideLoggers() loggerResult {
	zl, _ := zap.NewDevelopment()
	return loggerResult{
		Logger:        zl,
		SugaredLogger: zl.Sugar(),
	}
}

type serviceParams struct {
	fx.Out
	RepoManagerClonePath string `name:"repoManagerClonePath"`
	WorkerRootPath       string `name:"workerRootPath"`
	WorkerPoolSize       int    `name:"workerPoolSize"`
}

func provideGit(lc fx.Lifecycle, cfg *config.Config) (git.Interface, serviceParams, error) {
	repoManagerClonePath := cfg.Service.RepoManagerClonePath
	workerRootPath := cfg.Service.WorkerRootPath
	if err := os.MkdirAll(repoManagerClonePath, 0o755); err != nil {
		return nil, serviceParams{}, fmt.Errorf("failed to create repo manager clone path: %w", err)
	}
	if err := os.MkdirAll(workerRootPath, 0o755); err != nil {
		return nil, serviceParams{}, fmt.Errorf("failed to create worker root path: %w", err)
	}
	lc.Append(fx.StopHook(func() {
		os.RemoveAll(repoManagerClonePath)
		os.RemoveAll(workerRootPath)
	}))
	return git.New(repoManagerClonePath), serviceParams{
		RepoManagerClonePath: repoManagerClonePath,
		WorkerRootPath:       workerRootPath,
		WorkerPoolSize:       cfg.Service.WorkerPoolSize,
	}, nil
}

type appCtxResult struct {
	fx.Out
	AppCtx context.Context `name:"appCtx"`
}

func provideAppCtx(lc fx.Lifecycle) appCtxResult {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.StopHook(cancel))
	return appCtxResult{AppCtx: ctx}
}

type serverParams struct {
	fx.In
	Server pb.TangoYARPCServer
	Logger *zap.SugaredLogger
}

func startServer(lc fx.Lifecycle, p serverParams) error {
	grpcTransport := yarpcgrpc.NewTransport()
	port := "127.0.0.1:8081"
	grpcListener, err := net.Listen("tcp", port)
	if err != nil {
		return fmt.Errorf("failed to listen on gRPC port: %w", err)
	}
	dispatcher := yarpc.NewDispatcher(yarpc.Config{
		Name: "tango",
		Inbounds: []transport.Inbound{
			grpcTransport.NewInbound(grpcListener),
		},
	})
	dispatcher.Register(pb.BuildTangoYARPCProcedures(p.Server))

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			p.Logger.Infof("Tango server starting:")
			p.Logger.Infof("- gRPC inbound:  %s", port)
			return dispatcher.Start()
		},
		OnStop: func(context.Context) error {
			return dispatcher.Stop()
		},
	})
	return nil
}

func newStorage(cfg config.StorageConfig) (storage.Storage, error) {
	switch cfg.Type {
	case config.StorageTypeMemory, "":
		return storage.NewMemoryStorage(), nil
	case config.StorageTypeDisk:
		if cfg.Disk == nil {
			return nil, fmt.Errorf("disk storage requires 'disk' configuration")
		}
		if cfg.Disk.RootPath == "" {
			return nil, fmt.Errorf("disk storage requires 'root_path' to be set")
		}
		return disk.New(cfg.Disk.RootPath)
	default:
		return nil, fmt.Errorf("unsupported storage type: %q", cfg.Type)
	}
}
