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

package controller

import (
	"github.com/uber-go/tally"
	"github.com/uber/tango/config"
	"github.com/uber/tango/core/common"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/orchestrator"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// Params are the parameters for the controller.
type Params struct {
	fx.In
	Logger       *zap.Logger
	Storage      storage.Storage
	Orchestrator orchestrator.Orchestrator
	Scope        tally.Scope        `optional:"true"`
	ChunkConfig  config.ChunkConfig `optional:"true"`
}

type controller struct {
	logger                 *zap.Logger
	storage                storage.Storage
	orchestrator           orchestrator.Orchestrator
	scope                  tally.Scope
	targetChunkSize        int
	changedTargetChunkSize int
	metadataMapChunkSize   int
}

// NewController creates a new controller.
func NewController(p Params) pb.TangoYARPCServer {
	scope := p.Scope
	if scope == nil {
		scope = tally.NoopScope
	}
	targetChunkSize := p.ChunkConfig.TargetChunkSize
	if targetChunkSize <= 0 {
		targetChunkSize = common.DefaultTargetChunkSize
	}
	changedTargetChunkSize := p.ChunkConfig.ChangedTargetChunkSize
	if changedTargetChunkSize <= 0 {
		changedTargetChunkSize = common.DefaultChangedTargetChunkSize
	}
	metadataMapChunkSize := p.ChunkConfig.MetadataMapChunkSize
	if metadataMapChunkSize <= 0 {
		metadataMapChunkSize = common.DefaultMetadataMapChunkSize
	}
	return &controller{
		logger:                 p.Logger,
		storage:                p.Storage,
		orchestrator:           p.Orchestrator,
		scope:                  scope.SubScope("controller"),
		targetChunkSize:        targetChunkSize,
		changedTargetChunkSize: changedTargetChunkSize,
		metadataMapChunkSize:   metadataMapChunkSize,
	}
}
