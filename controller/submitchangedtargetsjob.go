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

package controller

import (
	"context"
	"time"

	"github.com/uber/tango/config"
	"github.com/uber/tango/core/cachekey"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/observability/metrics"
	"go.uber.org/zap"
)

// SubmitChangedTargetsJob starts a GetChangedTargets job unless one is already active.
func (c *controller) SubmitChangedTargetsJob(ctx context.Context, request entity.GetChangedTargetsRequest, repoCfg config.RepositoryConfig) (string, entity.JobStatus, error) {
	token := cachekey.GetChangedTargetsJobToken(request.First, request.Second, request.ExcludeFilesRegex, request.BypassCache)
	return c.submitJob(ctx, token, cachekey.GetChangedTargetsJobPath(token), repoCfg.RepositoryID, request.BypassCache,
		func(ctx context.Context, logger *zap.Logger) (string, error) {
			return c.computeChangedTargets(ctx, logger, request, repoCfg)
		})
}

func (c *controller) computeChangedTargets(ctx context.Context, logger *zap.Logger, request entity.GetChangedTargetsRequest, repoCfg config.RepositoryConfig) (string, error) {
	e := c.emitter.Tagged(map[string]string{metrics.TagRepo: repoCfg.RepositoryID})
	if !request.BypassCache {
		cacheKey, err := c.comparedTargetsKey(ctx, e, request, repoCfg.RepositoryID)
		if err != nil && !storage.IsNotFound(err) {
			return "", err
		}
		if err == nil {
			cached, err := c.readComparedTargetsAt(ctx, e, logger, cacheKey)
			if err != nil || len(cached) > 0 {
				return cacheKey, err
			}
		}
	}

	firstGraph, secondGraph, err := c.fetchTargetGraphs(ctx, e, logger, request, func(ctx context.Context, graphRequest entity.GetTargetGraphRequest) (storage.GraphReader, error) {
		return c.graphReaderFromJob(ctx, logger, graphRequest, repoCfg)
	})
	if err != nil {
		return "", err
	}
	responses, err := c.compareFetchedGraphs(ctx, e, logger, request.First.Remote, firstGraph, secondGraph, seedAttributesFor(repoCfg))
	if err != nil {
		if ctx.Err() != nil {
			err = context.Cause(ctx)
		}
		return "", err
	}
	cacheKey, err := c.writeComparedTargets(ctx, request, repoCfg.RepositoryID, responses)
	return cacheKey, err
}

func (c *controller) graphReaderFromJob(ctx context.Context, logger *zap.Logger, request entity.GetTargetGraphRequest, repoCfg config.RepositoryConfig) (storage.GraphReader, error) {
	token, status, err := c.SubmitTargetGraphJob(ctx, request, repoCfg)
	if err != nil {
		return nil, err
	}
	if status.State != entity.JobStateCompleted {
		logger.Info("Waiting for target graph job", zap.String("graph_job_token", token))
	}
	ticker := time.NewTicker(c.jobHeartbeatInterval)
	defer ticker.Stop()
	for {
		status, graphReader, err := c.PollTargetGraphJob(ctx, token)
		if err != nil {
			return nil, err
		}
		switch status.State {
		case entity.JobStateCompleted:
			return graphReader, nil
		case entity.JobStateFailed:
			return nil, jobStatusError(status)
		}
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-ticker.C:
		}
	}
}
