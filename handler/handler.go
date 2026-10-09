// Copyright (c) 2026 Uber Technologies, Inc.
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

package handler

import (
	"fmt"

	"github.com/uber-go/tally"
	"github.com/uber/tango/config"
	"github.com/uber/tango/controller"
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/observability/metrics"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

const unknownRepositoryMetricLabel = "unknown"

// Params are the parameters for the handler.
type Params struct {
	fx.In
	Logger     *zap.Logger
	Controller controller.Controller
	RepoConfig config.RepositoryConfigProvider
	Scope      tally.Scope `optional:"true"`
}

type handler struct {
	logger     *zap.Logger
	controller controller.Controller
	repoConfig config.RepositoryConfigProvider
	emitter    *metrics.Emitter
}

// New creates a new handler implementing the generated YARPC service surface.
// The emitter is rooted at the "handler" scope, so the RPC lifecycle metrics
// that the handler owns publish under handler.<op>. The controller keeps its
// own "controller" scope for its phase metrics.
func New(p Params) pb.TangoYARPCServer {
	return &handler{
		logger:     p.Logger,
		controller: p.Controller,
		repoConfig: p.RepoConfig,
		emitter:    metrics.New(p.Scope).SubScope("handler"),
	}
}

// resolveRequestRepository returns the configured repository and metric label
// for a validated request. Invalid requests retain the common unknown label and
// their existing error; valid requests must exactly match the configured
// repository allowlist before controller cache I/O.
func (h *handler) resolveRequestRepository(remote string, requestErr error) (config.RepositoryConfig, string, error) {
	if requestErr != nil {
		return config.RepositoryConfig{}, unknownRepositoryMetricLabel, requestErr
	}
	repo, ok := h.repoConfig.GetRepositoryConfig(remote)
	if !ok {
		return config.RepositoryConfig{}, unknownRepositoryMetricLabel, tangoerrors.NewUser(
			fmt.Errorf("repository remote %q is not configured", remote),
		)
	}
	return repo, repo.RepositoryID, nil
}
