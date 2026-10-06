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
	"errors"
	"fmt"

	"github.com/uber/tango/core/cachekey"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
	"go.uber.org/zap"
)

// PollTargetGraphJob returns the job status and, after COMPLETED, a reader over the target graph.
func (c *controller) PollTargetGraphJob(ctx context.Context, token string) (entity.JobStatus, storage.GraphReader, error) {
	ctx, cancelLink := c.linkRequestCtx(ctx)
	defer cancelLink()
	status, found, err := c.readJobStatus(ctx, cachekey.GetTargetGraphJobPath(token))
	if err != nil {
		return entity.JobStatus{}, nil, fmt.Errorf("read job status: %w", err)
	}
	if !found {
		return entity.JobStatus{}, nil, errJobNotFound
	}
	if status.State != entity.JobStateCompleted {
		return status, nil, nil
	}
	graphReader, err := c.openJobGraph(ctx, status)
	if err != nil {
		if storage.IsNotFound(err) || errors.Is(err, storage.ErrCorruptTGB) {
			c.logger.Warn("PollTargetGraphJob: result not readable",
				zap.String("repository", status.RepositoryID),
				zap.String("job_token", token),
				zap.Error(err),
			)
			return status, nil, errJobNotFound
		}
		return status, nil, fmt.Errorf("open graph: %w", err)
	}
	return status, graphReader, nil
}
