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
	"fmt"

	"github.com/uber/tango/core/cachekey"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/observability/metrics"
	"go.uber.org/zap"
)

// PollChangedTargetsJob returns the job status and, after COMPLETED, the changed-target chunks.
func (c *controller) PollChangedTargetsJob(ctx context.Context, token string) (entity.JobStatus, []entity.GetChangedTargetsResponse, error) {
	ctx, cancelLink := c.linkRequestCtx(ctx)
	defer cancelLink()
	status, found, err := c.readJobStatus(ctx, cachekey.GetChangedTargetsJobPath(token))
	if err != nil {
		return entity.JobStatus{}, nil, fmt.Errorf("read job status: %w", err)
	}
	if !found {
		return entity.JobStatus{}, nil, errJobNotFound
	}
	if status.State != entity.JobStateCompleted {
		return status, nil, nil
	}
	e := c.emitter.Tagged(map[string]string{metrics.TagRepo: status.RepositoryID})
	logger := c.logger.With(zap.String("repository", status.RepositoryID), zap.String("job_token", token))
	chunks, found, err := c.readComparedTargetsAt(ctx, e, logger, status.ResultKey)
	if err != nil {
		return status, nil, fmt.Errorf("read compared targets: %w", err)
	}
	if !found {
		return status, nil, errJobNotFound
	}
	return status, chunks, nil
}
