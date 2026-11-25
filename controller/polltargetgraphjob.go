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
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
)

// PollTargetGraphJob returns the job status and, after COMPLETED, a reader over the target graph.
func (c *controller) PollTargetGraphJob(ctx context.Context, token string) (entity.JobStatus, storage.GraphReader, error) {
	ctx, cancelLink := c.linkRequestCtx(ctx)
	defer cancelLink()
	key := cachekey.GetTargetGraphJobPath(token)
	status, err := c.jobStorage.Get(ctx, key)
	if storage.IsNotFound(err) {
		return entity.JobStatus{}, nil, tangoerrors.NewUser(err)
	}
	if err != nil {
		return entity.JobStatus{}, nil, fmt.Errorf("read job status: %w", err)
	}
	if c.isJobStale(status) {
		return status, nil, errStaleJob
	}
	if status.State != entity.JobStateCompleted {
		return status, nil, nil
	}
	graphReader, err := c.openJobGraph(ctx, status)
	if err != nil {
		return status, nil, fmt.Errorf("open graph: %w", err)
	}
	return status, graphReader, nil
}
