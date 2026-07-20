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
	"io"
	"time"

	"github.com/uber/tango/core/cachekey"
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/mapper"
	"github.com/uber/tango/internal/url"
	"github.com/uber/tango/observability/metrics"

	"github.com/uber/tango/core/storage"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/zap"
)

// GetTargetGraph returns the target graph for a given request.
func (c *controller) GetTargetGraph(request *pb.GetTargetGraphRequest, stream pb.TangoServiceGetTargetGraphYARPCServer) (retErr error) {
	repo := url.ToShortRemote(request.GetBuildDescription().GetRemote())
	e := c.emitter.Tagged(map[string]string{metrics.TagRepo: repo})
	op := metrics.Begin(e, opGetTargetGraph, slowDurationBuckets)
	logger := c.logger.WithLazy(
		zap.Any("build_description", request.GetBuildDescription()),
	)
	defer func() {
		op.Complete(retErr)
		if retErr != nil {
			logger.Error("GetTargetGraph failed", tangoerrors.Fields(retErr)...)
			emitFailureMetric(e, opGetTargetGraph, retErr)
		}
	}()
	start := time.Now()
	ctx, cancelLink := c.linkRequestCtx(stream.Context())
	defer cancelLink()
	entityReq, err := mapper.ProtoToGetTargetGraphRequest(request)
	if err != nil {
		return tangoerrors.NewUser(fmt.Errorf("convert get target graph request: %w", err))
	}
	graphReader, err := c.getGraph(ctx, e, entityReq)
	if err != nil {
		return fmt.Errorf("get graph: %w", err)
	}
	if graphReader == nil {
		// Nothing to stream
		return nil
	}
	defer func() { _ = graphReader.Close() }()
	sendStart := time.Now()
	outputConfig := request.GetOutputConfig()
	for {
		chunk, err := graphReader.Read()
		if err == io.EOF {
			sendDuration := time.Since(sendStart)
			logger.Info("GetTargetGraph: Done streaming",
				zap.Duration("send_duration", sendDuration),
				zap.Duration("total_duration", time.Since(start)),
			)
			e.DurationHistogram(opGetTargetGraph, "send_duration", fastDurationBuckets).RecordDuration(sendDuration)
			return nil
		}
		if err != nil {
			return fmt.Errorf("graph reader read: %w", err)
		}
		protoResp := mapper.GetTargetGraphResponseToProto(&chunk)
		toSend := applyOptimizedTargetsOutputConfigToChunk(protoResp, outputConfig)
		err = stream.Send(toSend)
		if err != nil {
			return fmt.Errorf("send graph: %w", err)
		}
	}
}

// getGraph retrieves the target graph for a given build description.
// Returns nil response for cache miss or empty response cases (to indicate no send should happen).
// OutputConfig is deliberately not part of the orchestrator request: cache
// entries store the full payload and stripping happens at send time, so
// letting an orchestrator see it could poison the shared cache with
// stripped graphs.
func (c *controller) getGraph(ctx context.Context, e *metrics.Emitter, req entity.GetTargetGraphRequest) (storage.GraphReader, error) {
	start := time.Now()
	logger := c.logger.With(
		zap.Any("build_description", req.Build),
	)
	if !req.BypassCache {
		// Look up the the git treehash based on cache path
		treehashCachePath := cachekey.GetTreehashCachePath(req.Build)
		treehashResponse, err := c.storage.Get(ctx, storage.DownloadRequest{Key: treehashCachePath})
		if err != nil {
			if storage.IsNotFound(err) {
				// Cache miss - blob doesn't exist, need to compute and store target graph
				logger.Debug("getGraph: treehash not found", zap.Error(err))
			} else {
				// Other errors (network, infra issues) should be retried
				return nil, fmt.Errorf("get treehash: %w", err)
			}
		} else {
			defer func() { _ = treehashResponse.ReadCloser.Close() }()
			treehashBytes, err := io.ReadAll(treehashResponse.ReadCloser)
			if err != nil {
				return nil, fmt.Errorf("read treehash: %w", err)
			}
			logger.Info("getGraph: treehash found")
			treehashPath := cachekey.GetGraphByTreeHash(req.Build.Remote, string(treehashBytes), req.Build.Strategy, req.ExcludeFilesRegex)
			// Download the target graph based on treehash.
			storageStart := time.Now()
			graphReader, err := storage.NewGraphReader(ctx, c.storage, treehashPath)
			if err != nil {
				if ctx.Err() != nil {
					err = ctx.Err()
				}
				if !storage.IsNotFound(err) {
					return nil, fmt.Errorf("graph reader: %w", err)
				}
				logger.Warn("getGraph: graph not found at treehash path", zap.Error(err))
			} else {
				logger.Info("getGraph: loaded graph from storage",
					zap.Duration("storage_duration", time.Since(storageStart)),
					zap.Duration("total_duration", time.Since(start)),
				)
				e.Counter(opGetTargetGraph, "cache_hit").Inc(1)
				e.DurationHistogram(opGetTargetGraph, "download_graph", slowDurationBuckets).RecordDuration(time.Since(storageStart))
				return graphReader, nil
			}
		}
	} else {
		logger.Info("getGraph: bypass_cache=true, skipping cache lookup")
	}
	computeStart := time.Now()
	graphReader, err := c.orchestrator.GetTargetGraph(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("get target graph: %w", err)
	}
	logger.Info("getGraph: computed target graph",
		zap.Duration("compute_duration", time.Since(computeStart)),
		zap.Duration("total_duration", time.Since(start)),
	)
	return graphReader, nil
}
