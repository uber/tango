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
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/uber/tango/config"
	"github.com/uber/tango/controller"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/repomanager"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/core/storage/disk"
	"github.com/uber/tango/orchestrator"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/yarpc"
	"go.uber.org/yarpc/api/transport"
	yarpcgrpc "go.uber.org/yarpc/transport/grpc"
	"go.uber.org/zap"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	logger, err := zap.NewDevelopment()
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer logger.Sync()

	// appCtx is the application-lifetime context. It is cancelled on
	// SIGINT/SIGTERM so background work (e.g. the controller's async
	// cache write) is aborted instead of leaking past process exit.
	appCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	configFilePath := filepath.Join("example", "tango-config.yaml")
	cfg, err := config.Parse(configFilePath)
	if err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	store, err := newStorage(cfg.Storage)
	if err != nil {
		return fmt.Errorf("failed to create storage: %w", err)
	}
	logger.Info("Using storage", zap.String("type", string(cfg.Storage.Type)))

	// Repo manager and orchestrator
	repoManagerClonePath := cfg.Service.WorkspacesRootPath
	if err := os.MkdirAll(repoManagerClonePath, 0o755); err != nil {
		return fmt.Errorf("failed to create repo manager clone path: %w", err)
	}
	defer os.RemoveAll(repoManagerClonePath)

	rm, err := repomanager.NewRepoManager(appCtx, repomanager.Params{
		Git:                  git.New(repoManagerClonePath, logger),
		Logger:               logger,
		RepoManagerClonePath: repoManagerClonePath,
		PoolSize:             cfg.Service.MaxWorkerPoolSize,
		RepoConfig:           cfg,
	})
	if err != nil {
		return fmt.Errorf("failed to create repo manager: %w", err)
	}
	orch, err := orchestrator.NewNativeOrchestrator(appCtx, orchestrator.Params{
		Storage:     store,
		RepoManager: rm,
		Logger:      logger,
		GitFactory:  func(dir string) git.Interface { return git.New(dir, logger) },
		Config:      cfg,
	})
	if err != nil {
		return fmt.Errorf("failed to setup orchestrator: %w", err)
	}

	// Controller (YARPC server implementation). appCtx is forwarded so the
	// controller's background goroutines are tied to process lifetime.
	ctrl := controller.NewController(appCtx, controller.Params{
		Logger:          logger,
		Storage:         store,
		Orchestrator:    orch,
		MaxMessageBytes: cfg.Service.MaxMessageBytes,
		RepoConfig:      cfg,
		GraphFormat:     cfg.Service.GraphFormat,
		ShadowCompare:   cfg.Service.ShadowCompare,
	})

	// YARPC transports and dispatcher
	grpcTransport := yarpcgrpc.NewTransport()
	port := "127.0.0.1:8081"
	grpcListener, err := net.Listen("tcp", port)
	if err != nil {
		return fmt.Errorf("failed to listen on gRPC port: %w", err)
	}

	inbounds := []transport.Inbound{
		grpcTransport.NewInbound(grpcListener),
	}
	dispatcher := yarpc.NewDispatcher(yarpc.Config{
		Name:     "tango",
		Inbounds: inbounds,
	})
	dispatcher.Register(pb.BuildTangoYARPCProcedures(ctrl))

	if err := dispatcher.Start(); err != nil {
		return fmt.Errorf("failed to start dispatcher: %w", err)
	}
	defer func() { _ = dispatcher.Stop() }()

	logger.Info("Tango server is running", zap.String("grpc_inbound", port))
	logger.Info("Press Ctrl+C to stop.")
	// Block until SIGINT/SIGTERM cancels appCtx.
	<-appCtx.Done()
	return nil
}

// newStorage creates a Storage implementation based on the provided configuration.
// Storage type and disk.root_path are validated by config.Parse, so the switch
// only needs to handle the known types.
func newStorage(cfg config.StorageConfig) (storage.Storage, error) {
	switch cfg.Type {
	case config.StorageTypeMemory:
		return storage.NewMemoryStorage(), nil
	case config.StorageTypeDisk:
		return disk.New(cfg.Disk.RootPath)
	default:
		// Unreachable after config.Parse validation, but kept for safety.
		return nil, fmt.Errorf("unsupported storage type: %q", cfg.Type)
	}
}
