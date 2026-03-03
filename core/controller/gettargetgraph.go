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
	"github.com/uber/tango/core/common"
	"github.com/uber/tango/orchestrator"
	"io"

	"github.com/uber/tango/core/storage"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/zap"
)

// GetTargetGraph returns the target graph for a given request.
func (c *controller) GetTargetGraph(request *pb.GetTargetGraphRequest, stream pb.TangoServiceGetTargetGraphYARPCServer) error {
	ctx := stream.Context()
	graphReader, err := c.getGraph(ctx, request.GetBuildDescription(), request.GetOutputConfig())
	if err != nil {
		return err
	}
	if graphReader == nil {
		// Nothing to stream
		return nil
	}
	defer graphReader.Close()
	for {
		graphStreamChunk, err := graphReader.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		err = stream.Send(graphStreamChunk)
		if err != nil {
			return fmt.Errorf("send graph: %w", err)
		}
	}
}

// getGraph retrieves the target graph for a given build description and output config.
// Returns nil response for cache miss or empty response cases (to indicate no send should happen).
// TODO: remove output config from input parameters if not used in future.
func (c *controller) getGraph(ctx context.Context, buildDescription *pb.BuildDescription, outputConfig *pb.OutputConfig) (storage.GraphReader, error) {
	if buildDescription == nil {
		return nil, errors.New("build description is empty or invalid")
	}
	if buildDescription.GetBaseSha() == "" || buildDescription.GetRemote() == "" {
		return nil, fmt.Errorf("build description is missing required fields: base_sha: %s, remote: %s", buildDescription.GetBaseSha(), buildDescription.GetRemote())
	}
	// Look up the the git treehash based on cache path
	treehashCachePath := common.GetTreehashCachePath(buildDescription)
	treehashResponse, err := c.storage.Get(ctx, storage.DownloadRequest{Key: treehashCachePath})
	if err != nil {
		if storage.IsNotFound(err) {
			// Cache miss - blob doesn't exist, need to compute and store target graph
			c.logger.Info("getGraph: treehash not found", zap.Any("request build description", buildDescription), zap.Error(err))
			graphReader, err := c.orchestrator.GetTargetGraph(ctx, orchestrator.GetTargetGraphParam{Req: &pb.GetTargetGraphRequest{BuildDescription: buildDescription, OutputConfig: outputConfig}})
			if err != nil {
				return nil, err
			}
			return graphReader, nil
		}
		// Other errors (network, infra issues) should be retried
		c.logger.Error("getGraph: Storage error", zap.Any("request build description", buildDescription), zap.Error(err))
		return nil, err
	}
	if treehashResponse == nil || treehashResponse.ReadCloser == nil {
		// This shouldn't happen with valid Storage implementation, but handle gracefully
		c.logger.Info("getGraph: Empty response from Storage", zap.Any("request build description", buildDescription))
		return nil, nil // Return nil to indicate no send should happen
	}
	defer treehashResponse.ReadCloser.Close()
	treehashBytes, err := io.ReadAll(treehashResponse.ReadCloser)
	if err != nil {
		c.logger.Error("getGraph: Error reading treehash", zap.Any("request build description", buildDescription), zap.Error(err))
		return nil, err
	}

	c.logger.Info("getGraph: treehash found", zap.Any("request build description", buildDescription))
	treehashPath := common.GetGraphByTreeHash(buildDescription.GetRemote(), string(treehashBytes))
	// Download the target graph based on treehash.
	graphReader, err := storage.NewGraphReader(ctx, c.storage, treehashPath)
	if err != nil {
		c.logger.Error("getGraph: Error reading graph from Storage", zap.Any("request build description", buildDescription), zap.Error(err))
		return nil, err
	}
	return graphReader, nil
}
