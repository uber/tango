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
	"context"
	"fmt"
	"time"

	"github.com/uber-go/tally"
	"github.com/uber/tango/config"
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

// _totalDurationBuckets covers 0–15 minutes in 10-second linear intervals.
var _totalDurationBuckets = tally.MustMakeLinearDurationBuckets(10*time.Second, 10*time.Second, 90)

type controller struct {
	logger                 *zap.Logger
	storage                storage.Storage
	orchestrator           orchestrator.Orchestrator
	scope                  tally.Scope
	targetChunkSize        int
	changedTargetChunkSize int
	metadataMapChunkSize   int
	totalDurationBuckets   tally.Buckets

	// appCtx is the application lifetime; cancel it on process shutdown.
	// Used by linkRequestCtx and any fire-and-forget goroutines so they
	// abort instead of leaking past server teardown.
	appCtx context.Context
}

// NewController creates a new controller. appCtx is cancelled on process
// shutdown to abort background work.
func NewController(appCtx context.Context, p Params) (pb.TangoYARPCServer, error) {
	scope := p.Scope
	if scope == nil {
		scope = tally.NoopScope
	}
	if p.ChunkConfig.TargetChunkSize <= 0 {
		return nil, fmt.Errorf("target_chunk_size must be > 0, got %d", p.ChunkConfig.TargetChunkSize)
	}
	if p.ChunkConfig.ChangedTargetChunkSize <= 0 {
		return nil, fmt.Errorf("changed_target_chunk_size must be > 0, got %d", p.ChunkConfig.ChangedTargetChunkSize)
	}
	if p.ChunkConfig.MetadataMapChunkSize <= 0 {
		return nil, fmt.Errorf("metadata_map_chunk_size must be > 0, got %d", p.ChunkConfig.MetadataMapChunkSize)
	}
	return &controller{
		logger:                 p.Logger,
		storage:                p.Storage,
		orchestrator:           p.Orchestrator,
		scope:                  scope.SubScope("controller"),
		targetChunkSize:        p.ChunkConfig.TargetChunkSize,
		changedTargetChunkSize: p.ChunkConfig.ChangedTargetChunkSize,
		metadataMapChunkSize:   p.ChunkConfig.MetadataMapChunkSize,
		totalDurationBuckets:   _totalDurationBuckets,
		appCtx:                 appCtx,
	}, nil
}

// linkRequestCtx returns a context derived from reqCtx that is also cancelled
// when c.appCtx is cancelled. Use it at the top of every streaming handler
// (and pass the returned context to all downstream calls) so a request is
// aborted both by the client disconnecting (reqCtx) and by the server
// beginning to shut down (appCtx).
//
// The returned cancel function MUST be deferred; it releases the
// context.AfterFunc handle so we do not leak a watcher past the request.
func (c *controller) linkRequestCtx(reqCtx context.Context) (context.Context, context.CancelFunc) {
	// Derive a per-request ctx whose cancel only affects this ctx and its
	// children — it never propagates up to reqCtx.
	ctx, cancel := context.WithCancel(reqCtx)
	// Register a one-shot watcher that cancels the derived ctx if appCtx fires.
	// AfterFunc only observes appCtx; it never cancels it. stop() deregisters
	// the watcher so the closure is not retained past the request.
	stop := context.AfterFunc(c.appCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
