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
	defer func() {
		if retErr != nil {
			scope.Counter("failure").Inc(1)
			emitFailureMetric(scope, retErr)
		} else {
			scope.Counter("success").Inc(1)
		}
	}()
	start := time.Now()
	ctx := stream.Context()
	logger := c.logger.With(
		zap.Any("build_description", request.GetBuildDescription()),
	)
	scope = scope.Tagged(map[string]string{"repo": common.ToShortRemote(request.GetBuildDescription().GetRemote())})
	graphReader, err := c.getGraph(ctx, request.GetBuildDescription(), request.GetOutputConfig(), request.GetRequestOptions(), request.GetBypassCache())
	if err != nil {
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
			logger.Info("GetTargetGraph: Done streaming",
				zap.Duration("send_duration", sendDuration),
				zap.Duration("total_duration", totalDuration),
			)
			scope.Timer("send_duration").Record(sendDuration)
			scope.Timer("total_duration").Record(totalDuration)
			return nil
		}
		if err != nil {
			return common.WithReason(failureReasonGraphFetch, common.ErrorTypeInfra, err)
		}
		toSend := applyOptimizedTargetsOutputConfigToChunk(graphStreamChunk, outputConfig)
		err = stream.Send(toSend)
		if err != nil {
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
	logger := c.logger.With(
		zap.Any("build_description", buildDescription),
	)
	if !bypassCache {
		// Look up the the git treehash based on cache path
		treehashCachePath := common.GetTreehashCachePath(buildDescription)
		treehashResponse, err := c.storage.Get(ctx, storage.DownloadRequest{Key: treehashCachePath})
		if err != nil {
			if storage.IsNotFound(err) {
				// Cache miss - blob doesn't exist, need to compute and store target graph
				logger.Info("getGraph: treehash not found", zap.Error(err))
			} else {
				// Other errors (network, infra issues) should be retried
				logger.Error("getGraph: Storage error", zap.Error(err))
				return nil, err
			}
		} else if treehashResponse == nil || treehashResponse.ReadCloser == nil {
			// This shouldn't happen with valid Storage implementation, but handle gracefully
			logger.Info("getGraph: Empty response from Storage")
			return nil, nil // Return nil to indicate no send should happen
		} else {
			defer treehashResponse.ReadCloser.Close()
			treehashBytes, err := io.ReadAll(treehashResponse.ReadCloser)
			if err != nil {
				logger.Error("getGraph: Error reading treehash", zap.Error(err))
				return nil, err
			}
			logger.Info("getGraph: treehash found")
			treehashPath := common.GetGraphByTreeHash(buildDescription.GetRemote(), string(treehashBytes), buildDescription.GetStrategy(), requestOptions)
			// Download the target graph based on treehash.
			storageStart := time.Now()
			graphReader, err := storage.NewGraphReader(ctx, c.storage, treehashPath)
			if err != nil {
				logger.Error("getGraph: Error reading graph from Storage", zap.Error(err))
				return nil, err
			}
			logger.Info("getGraph: loaded graph from storage",
				zap.Duration("storage_duration", time.Since(storageStart)),
				zap.Duration("total_duration", time.Since(start)),
			)
			scope := c.scope.SubScope("get_graph")
			scope.Counter("cache_hit").Inc(1)
			scope.Timer("storage_duration").Record(time.Since(storageStart))
			scope.Timer("total_duration").Record(time.Since(start))
			return graphReader, nil
		}
	} else {
		logger.Info("getGraph: bypass_cache=true, skipping cache lookup")
	}
	computeStart := time.Now()
	graphReader, err := c.orchestrator.GetTargetGraph(ctx, orchestrator.GetTargetGraphParam{Req: &pb.GetTargetGraphRequest{BuildDescription: buildDescription, OutputConfig: outputConfig, RequestOptions: requestOptions}, BypassCache: bypassCache})
	if err != nil {
		return nil, err
	}
	logger.Info("getGraph: computed target graph",
		zap.Duration("compute_duration", time.Since(computeStart)),
		zap.Duration("total_duration", time.Since(start)),
	)
	scope := c.scope.SubScope("get_graph")
	scope.Timer("compute_duration").Record(time.Since(computeStart))
	scope.Timer("total_duration").Record(time.Since(start))
	return graphReader, nil
}
