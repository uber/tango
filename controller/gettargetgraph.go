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
	"fmt"
	"io"
	"time"

	"github.com/uber/tango/core/common"
	"github.com/uber/tango/orchestrator"

	"github.com/uber/tango/core/storage"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/zap"
)

// GetTargetGraph returns the target graph for a given request.
func (c *controller) GetTargetGraph(request *pb.GetTargetGraphRequest, stream pb.TangoServiceGetTargetGraphYARPCServer) (retErr error) {
	scope := c.scope.SubScope("get_target_graph")
	scope.Counter("calls").Inc(1)
	start := time.Now()
	ctx, cancelLink := c.linkRequestCtx(stream.Context())
	defer cancelLink()
	logger := c.logger.With(
		zap.Any("build_description", request.GetBuildDescription()),
	)
	defer func() {
		if retErr != nil {
			scope.Counter("failure").Inc(1)
			emitFailureMetric(scope, retErr)
		} else {
			scope.Counter("success").Inc(1)
		}
	}()
	scope = scope.Tagged(map[string]string{"repo": common.ToShortRemote(request.GetBuildDescription().GetRemote())})
	graphReader, err := c.getGraph(ctx, request.GetBuildDescription(), request.GetOutputConfig(), request.GetRequestOptions(), request.GetBypassCache())
	if err != nil {
		logger.Error("GetTargetGraph: failed to get graph", zap.Error(err))
		return err
	}
	if graphReader == nil {
		// Nothing to stream
		return nil
	}
	defer graphReader.Close()
	sendStart := time.Now()
	outputConfig := request.GetOutputConfig()
	for {
		graphStreamChunk, err := graphReader.Read()
		if err == io.EOF {
			sendDuration := time.Since(sendStart)
			totalDuration := time.Since(start)
			scope.Timer("send_duration").Record(sendDuration)
			scope.Timer("total_duration").Record(totalDuration)
			return nil
		}
		if err != nil {
			logger.Error("GetTargetGraph: failed to read graph stream", zap.Error(err))
			return common.WithReason(failureReasonGraphFetch, common.ErrorTypeInfra, err)
		}
		toSend := applyOptimizedTargetsOutputConfigToChunk(graphStreamChunk, outputConfig)
		err = stream.Send(toSend)
		if err != nil {
			logger.Error("GetTargetGraph: failed to send graph", zap.Error(err))
			return common.WithReason(failureReasonSend, common.ErrorTypeInfra, fmt.Errorf("send graph: %w", err))
		}
	}
}

// getGraph retrieves the target graph for a given build description and output config.
// Returns nil response for cache miss or empty response cases (to indicate no send should happen).
// TODO: remove output config from input parameters if not used in future.
func (c *controller) getGraph(ctx context.Context, buildDescription *pb.BuildDescription, outputConfig *pb.OutputConfig, requestOptions *pb.RequestOptions, bypassCache bool) (storage.GraphReader, error) {
	start := time.Now()
	if buildDescription == nil {
		return nil, errors.New("build description is empty or invalid")
	}
	if buildDescription.GetBaseSha() == "" || buildDescription.GetRemote() == "" {
		return nil, fmt.Errorf("build description is missing required fields: base_sha: %s, remote: %s", buildDescription.GetBaseSha(), buildDescription.GetRemote())
	}
	if !bypassCache {
		// Look up the the git treehash based on cache path
		treehashCachePath := common.GetTreehashCachePath(buildDescription)
		treehashResponse, err := c.storage.Get(ctx, storage.DownloadRequest{Key: treehashCachePath})
		if err != nil {
			if storage.IsNotFound(err) {
				// Cache miss - fall through to compute
			} else {
				return nil, fmt.Errorf("storage error reading treehash: %w", err)
			}
		} else {
			defer treehashResponse.ReadCloser.Close()
			treehashBytes, err := io.ReadAll(treehashResponse.ReadCloser)
			if err != nil {
				return nil, fmt.Errorf("read treehash: %w", err)
			}
			treehashPath := common.GetGraphByTreeHash(buildDescription.GetRemote(), string(treehashBytes), buildDescription.GetStrategy(), requestOptions)
			// Download the target graph based on treehash.
			storageStart := time.Now()
			graphReader, err := storage.NewGraphReader(ctx, c.storage, treehashPath)
			if err != nil {
				if ctx.Err() != nil {
					return nil, fmt.Errorf("context cancelled: %w", ctx.Err())
				}
				if !storage.IsNotFound(err) {
					return nil, fmt.Errorf("read graph from storage: %w", err)
				}
			} else {
				scope := c.scope.SubScope("get_graph")
				scope.Counter("graph_cache_hit").Inc(1)
				scope.Timer("storage_duration").Record(time.Since(storageStart))
				scope.Timer("total_duration").Record(time.Since(start))
				return graphReader, nil
			}
		}
	}
	computeStart := time.Now()
	graphReader, err := c.orchestrator.GetTargetGraph(ctx, orchestrator.GetTargetGraphParam{Req: &pb.GetTargetGraphRequest{BuildDescription: buildDescription, OutputConfig: outputConfig, RequestOptions: requestOptions}, BypassCache: bypassCache})
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("context cancelled: %w", ctx.Err())
		}
		return nil, fmt.Errorf("compute target graph: %w", err)
	}
	scope := c.scope.SubScope("get_graph")
	scope.Timer("compute_duration").Record(time.Since(computeStart))
	scope.Timer("total_duration").Record(time.Since(start))
	return graphReader, nil
}
