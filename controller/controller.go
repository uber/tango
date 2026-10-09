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
	"errors"

	"github.com/uber-go/tally"
	"github.com/uber/tango/config"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/observability/metrics"
	"github.com/uber/tango/orchestrator"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// Controller is Tango's entity-level business logic surface: cache reads and
// writes, orchestrator calls, and graph comparison. It speaks only entity
// types and returns full, unfiltered results. Proto conversion, request
// validation, output filtering, and response chunking for the wire are the
// handler layer's responsibility.
type Controller interface {
	// GetTargetGraph returns a reader over the target graph for req. It
	// returns a nil reader and a nil error when there is nothing to stream.
	GetTargetGraph(ctx context.Context, req entity.GetTargetGraphRequest, repo config.RepositoryConfig) (storage.GraphReader, error)
	// GetChangedTargets returns the full, unfiltered set of changes between
	// req's two revisions in a single, unchunked result. Splitting it into
	// wire-sized response chunks is the handler's responsibility.
	GetChangedTargets(ctx context.Context, req entity.GetChangedTargetsRequest, repo config.RepositoryConfig) (entity.ChangedTargetsResult, error)
}

// Params are the parameters for the controller.
type Params struct {
	fx.In
	Logger       *zap.Logger
	Storage      storage.Storage
	Orchestrator orchestrator.Orchestrator
	Scope        tally.Scope `optional:"true"`
	// MaxMessageBytes bounds two storage-format concerns, not the wire: the
	// decoded chunk size the TGB graph reader uses when re-chunking a cached
	// graph for GetTargetGraph (storage.NewTGBGraphReader), and the chunk
	// size the compared-targets cache write uses when splitting a computed
	// GetChangedTargets result before it is written to storage, so an older
	// binary reading the same cache entry can still forward each stored
	// chunk to the wire as one message. Response chunking for the wire
	// itself happens in the handler, against the same configured budget.
	MaxMessageBytes int                        `optional:"true"`
	GraphConfig     config.GraphConfigProvider `optional:"true"`
}

type controller struct {
	logger          *zap.Logger
	storage         storage.Storage
	orchestrator    orchestrator.Orchestrator
	emitter         *metrics.Emitter
	maxMessageBytes int
	graphConfig     config.GraphConfigProvider

	// appCtx is the application lifetime; cancel it on process shutdown.
	// Used by linkRequestCtx and any fire-and-forget goroutines so they
	// abort instead of leaking past server teardown.
	appCtx context.Context
}

// NewController creates a new Controller. appCtx is cancelled on process
// shutdown to abort background work.
func NewController(appCtx context.Context, p Params) Controller {
	emitter := metrics.New(p.Scope).SubScope("controller")
	maxMessageBytes := p.MaxMessageBytes
	if maxMessageBytes <= 0 {
		maxMessageBytes = config.DefaultMaxMessageBytes
	}
	return &controller{
		logger:          p.Logger,
		storage:         p.Storage,
		orchestrator:    p.Orchestrator,
		emitter:         emitter,
		maxMessageBytes: maxMessageBytes,
		graphConfig:     p.GraphConfig,
		appCtx:          appCtx,
	}
}

// linkRequestCtx returns a context derived from reqCtx that is also cancelled
// when c.appCtx is cancelled. Use it at the top of GetTargetGraph and
// GetChangedTargets (and pass the returned context to all downstream calls)
// so a request is aborted both by the caller disconnecting (reqCtx) and by
// the server beginning to shut down (appCtx).
//
// The returned cancel function MUST be deferred; it releases the
// context.AfterFunc handle so we do not leak a watcher past the request.
func (c *controller) linkRequestCtx(reqCtx context.Context) (context.Context, context.CancelFunc) {
	// Derive a per-request ctx whose cancel only affects this ctx and its
	// children — it never propagates up to reqCtx.
	ctx, cancel := context.WithCancelCause(reqCtx)
	// Register a one-shot watcher that cancels the derived ctx if appCtx fires.
	// AfterFunc only observes appCtx; it never cancels it. stop() deregisters
	// the watcher so the closure is not retained past the request.
	stop := context.AfterFunc(c.appCtx, func() { cancel(errors.New("app context canceled")) })
	return ctx, func() {
		stop()
		cancel(nil)
	}
}

// graphFormatFor returns the configured graph format for the given remote,
// defaulting to gob when no GraphConfigProvider is set.
func (c *controller) graphFormatFor(remote string) (string, error) {
	if c.graphConfig == nil {
		return config.GraphFormatGob, nil
	}
	gc, err := c.graphConfig.GetGraphConfig(remote)
	if err != nil {
		return "", err
	}
	return gc.Format, nil
}

// shadowCompareFor returns whether shadow comparison is enabled for the given
// remote. Returns false when no GraphConfigProvider is set or the remote has
// no config entry.
func (c *controller) shadowCompareFor(remote string) bool {
	if c.graphConfig == nil {
		return false
	}
	gc, err := c.graphConfig.GetGraphConfig(remote)
	if err != nil {
		return false
	}
	return gc.ShadowCompare
}
