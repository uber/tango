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
	"time"

	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
	"go.uber.org/zap"
)

func (c *controller) setJobStatus(ctx context.Context, key string, status entity.JobStatus) error {
	ctx, cancel := context.WithTimeout(ctx, c.jobHeartbeatInterval)
	defer cancel()
	status.LastUpdated = time.Now()
	return c.jobStorage.Put(ctx, key, status)
}

var errStaleJob = errors.New("job has no live worker")

func (c *controller) isJobStale(status entity.JobStatus) bool {
	return status.State == entity.JobStateRunning && time.Since(status.LastUpdated) > 3*c.jobHeartbeatInterval
}

func (c *controller) submitJob(ctx context.Context, token, key, repositoryID string, bypassCache bool, compute func(context.Context, *zap.Logger) (string, error)) (string, entity.JobStatus, error) {
	logger := c.logger.With(zap.String("repository", repositoryID), zap.String("job_token", token))
	status, err := c.jobStorage.Get(ctx, key)
	if err != nil && !storage.IsNotFound(err) {
		return "", entity.JobStatus{}, fmt.Errorf("read job status: %w", err)
	}
	found := err == nil
	if found && status.State == entity.JobStateRunning && !c.isJobStale(status) {
		logger.Info("Job already active")
		return token, status, nil
	}
	if found && status.State == entity.JobStateCompleted && !bypassCache {
		exists, err := c.storage.Exists(ctx, status.ResultKey)
		if err != nil {
			return "", entity.JobStatus{}, fmt.Errorf("check job result: %w", err)
		}
		if exists {
			logger.Info("Job already completed")
			return token, status, nil
		}
	}

	running := entity.JobStatus{State: entity.JobStateRunning, RepositoryID: repositoryID}
	if err := c.setJobStatus(ctx, key, running); err != nil {
		return "", entity.JobStatus{}, fmt.Errorf("write job status: %w", err)
	}
	logger.Info("Job submitted")
	go func() {
		jobCtx, cancel := context.WithTimeout(c.appCtx, c.jobTimeout)
		defer cancel()
		c.runJob(jobCtx, logger, key, repositoryID, compute)
	}()
	return token, running, nil
}

func (c *controller) runJob(ctx context.Context, logger *zap.Logger, key, repositoryID string, compute func(context.Context, *zap.Logger) (string, error)) {
	type outcome struct {
		resultKey string
		err       error
	}
	done := make(chan outcome, 1)
	go func() {
		resultKey, err := compute(ctx, logger)
		done <- outcome{resultKey: resultKey, err: err}
	}()

	ticker := time.NewTicker(c.jobHeartbeatInterval)
	defer ticker.Stop()
	var resultKey string
	var err error
wait:
	for {
		select {
		case <-ticker.C:
			if err := c.setJobStatus(ctx, key, entity.JobStatus{State: entity.JobStateRunning, RepositoryID: repositoryID}); err != nil {
				logger.Warn("Failed to renew job status", zap.Error(err))
			}
		case o := <-done:
			resultKey, err = o.resultKey, o.err
			break wait
		case <-ctx.Done():
			err = context.Cause(ctx)
			break wait
		}
	}

	final := entity.JobStatus{
		State:        entity.JobStateCompleted,
		RepositoryID: repositoryID,
		ResultKey:    resultKey,
	}
	if err != nil {
		final = entity.JobStatus{
			State:        entity.JobStateFailed,
			ErrorCode:    tangoerrors.GetErrorCode(err),
			ErrorMessage: err.Error(),
			RepositoryID: repositoryID,
		}
	}
	if err := c.setJobStatus(context.WithoutCancel(ctx), key, final); err != nil {
		logger.Warn("Failed to write final job status", zap.Stringer("state", final.State), zap.Error(err))
	}
}

func jobStatusError(status entity.JobStatus) error {
	err := errors.New(status.ErrorMessage)
	switch status.ErrorCode {
	case tangoerrors.ErrorUser:
		return tangoerrors.NewUser(err)
	case tangoerrors.ErrorInfraRetryable:
		return tangoerrors.NewInfraRetryable(err)
	case tangoerrors.ErrorCancelled:
		return tangoerrors.NewCancelled(err)
	default:
		return tangoerrors.NewInfra(err)
	}
}
