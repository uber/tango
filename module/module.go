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

// Package module provides the Fx dependency injection wiring for all Tango layers.
package module

import (
	"github.com/uber/tango/controller"
	"github.com/uber/tango/core/repomanager"
	"github.com/uber/tango/orchestrator"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/fx"
)

// Module wires all Tango service layers: controller, orchestrator, and repo manager.
//
// Callers must supply the following values before including this module:
//   - context.Context named "appCtx" — the application-lifetime context (used by the controller for background work)
//   - *zap.Logger and *zap.SugaredLogger — structured loggers
//   - storage.Storage — blob storage backend
//   - config.RepositoryConfigProvider — repository configuration
//   - git.Interface — git CLI wrapper (for repo manager origin operations)
//   - string named "repoManagerClonePath" — directory for origin clones
//   - string named "workerRootPath" — directory for worker clones
//   - int named "workerPoolSize" — max workers per repo
var Module = fx.Module("tango",
	fx.Provide(
		repomanager.NewRepoManager,
		fx.Annotate(
			orchestrator.NewNativeOrchestrator,
			fx.As(new(orchestrator.Orchestrator)),
		),
		fx.Annotate(
			controller.NewController,
			fx.As(new(pb.TangoYARPCServer)),
		),
	),
)
