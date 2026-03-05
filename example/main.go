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
	zl, _ := zap.NewDevelopment()
	defer zl.Sync()
	logger := zl.Sugar()

	configFilePath := filepath.Join("example", "tango-config.yaml")
	cfg, err := config.Parse(configFilePath)
	if err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	store, err := newStorage(cfg.Storage)
	if err != nil {
		return fmt.Errorf("failed to create storage: %w", err)
	}
	logger.Infof("Using storage type: %s", cfg.Storage.Type)

	// Repo manager and orchestrator
	repoManagerClonePath := cfg.Service.RepoManagerClonePath
	workerRootPath := cfg.Service.WorkerRootPath
	if err := os.MkdirAll(repoManagerClonePath, 0o755); err != nil {
		return fmt.Errorf("failed to create repo manager clone path: %w", err)
	}
	defer os.RemoveAll(repoManagerClonePath)
	if err := os.MkdirAll(workerRootPath, 0o755); err != nil {
		return fmt.Errorf("failed to create worker root path: %w", err)
	}
	defer os.RemoveAll(workerRootPath)

	rm := repomanager.NewRepoManager(repomanager.Params{
		Git:                  git.New(repoManagerClonePath),
		Logger:               logger,
		RepoManagerClonePath: repoManagerClonePath,
		WorkerRootPath:       workerRootPath,
		PoolSize:             cfg.Service.WorkerPoolSize,
	})
	orch := orchestrator.NewNativeOrchestrator(orchestrator.Params{
		Storage:        store,
		RepoManager:    rm,
		Logger:         logger,
		GitFactory:     git.New,
		ConfigFilePath: configFilePath,
	})

	// Controller (YARPC server implementation)
	ctrl := controller.NewController(controller.Params{
		Logger:       zl,
		Storage:      store,
		Orchestrator: orch,
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
	defer dispatcher.Stop()

	logger.Infof("Tango server is running:")
	logger.Infof("- gRPC inbound:  %s", port)
	logger.Infof("Press Ctrl+C to stop.")
	// Wait for interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	return nil
}

// newStorage creates a Storage implementation based on the provided configuration.
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
