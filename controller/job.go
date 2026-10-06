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

func (c *controller) readJobStatus(ctx context.Context, key string) (entity.JobStatus, bool, error) {
	return storage.ReadJobStatus(ctx, c.jobStorage, key, c.jobHeartbeatInterval)
}

func (c *controller) setJobStatus(ctx context.Context, logger *zap.Logger, key string, status entity.JobStatus) {
	ctx, cancel := context.WithTimeout(ctx, c.jobHeartbeatInterval)
	defer cancel()
	status.LastUpdated = time.Now()
	if err := storage.WriteJobStatus(ctx, c.jobStorage, key, status); err != nil {
		logger.Warn("setJobStatus: failed to write job status",
			zap.String("key", key),
			zap.Stringer("state", status.State),
			zap.Error(err),
		)
	}
}

// submitJob starts a job that runs compute, unless the job at key is RUNNING, or
// COMPLETED with its result still in storage. bypassCache always starts a job
// that is not RUNNING. The deadline of ctx is the job timeout.
func (c *controller) submitJob(ctx context.Context, token, key, repositoryID string, bypassCache bool, compute func(context.Context, *zap.Logger) (jobResult, error)) (string, entity.JobStatus, error) {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		return "", entity.JobStatus{}, tangoerrors.NewUser(errors.New("submit requires a request deadline"))
	}
	logger := c.logger.With(zap.String("repository", repositoryID), zap.String("job_token", token))
	status, found, err := c.readJobStatus(ctx, key)
	if err != nil {
		return "", entity.JobStatus{}, fmt.Errorf("read job status: %w", err)
	}
	if found && status.State == entity.JobStateRunning {
		logger.Info("Job already active", zap.Stringer("state", status.State))
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

	running := entity.JobStatus{State: entity.JobStateRunning, LastUpdated: time.Now(), RepositoryID: repositoryID}
	if err := storage.WriteJobStatus(ctx, c.jobStorage, key, running); err != nil {
		return "", entity.JobStatus{}, fmt.Errorf("write job status: %w", err)
	}
	logger.Info("Job submitted")
	go func() {
		jobCtx, cancel := context.WithDeadline(c.appCtx, deadline)
		defer cancel()
		c.runJob(jobCtx, logger, key, repositoryID, compute)
	}()
	return token, running, nil
}

type jobResult struct {
	key         string
	graphFormat string
}

// runJob runs compute and is the only writer of the job status after the submit.
// A running job renews the status every heartbeat interval, so a reader can tell a
// job without a live worker from the age of LastUpdated.
func (c *controller) runJob(ctx context.Context, logger *zap.Logger, key, repositoryID string, compute func(context.Context, *zap.Logger) (jobResult, error)) {
	type outcome struct {
		result jobResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := compute(ctx, logger)
		done <- outcome{result: result, err: err}
	}()

	ticker := time.NewTicker(c.jobHeartbeatInterval)
	defer ticker.Stop()
	var result jobResult
	var err error
wait:
	for {
		select {
		case <-ticker.C:
			c.setJobStatus(ctx, logger, key, entity.JobStatus{State: entity.JobStateRunning, RepositoryID: repositoryID})
		case o := <-done:
			result, err = o.result, o.err
			break wait
		case <-ctx.Done():
			err = context.Cause(ctx)
			break wait
		}
	}

	final := entity.JobStatus{
		State:        entity.JobStateCompleted,
		RepositoryID: repositoryID,
		ResultKey:    result.key,
		GraphFormat:  result.graphFormat,
	}
	if err != nil {
		final = entity.JobStatus{
			State:        entity.JobStateFailed,
			ErrorCode:    tangoerrors.GetErrorCode(err),
			ErrorMessage: err.Error(),
			RepositoryID: repositoryID,
		}
	}
	// Write the final status even after the job context ends.
	c.setJobStatus(context.WithoutCancel(ctx), logger, key, final)
}

// waitForJob reads the job status every heartbeat interval until the job is
// COMPLETED. A FAILED job, or a job without a live worker, returns an error.
func (c *controller) waitForJob(ctx context.Context, key string) (entity.JobStatus, error) {
	ticker := time.NewTicker(c.jobHeartbeatInterval)
	defer ticker.Stop()
	for {
		status, found, err := c.readJobStatus(ctx, key)
		if err != nil {
			return entity.JobStatus{}, fmt.Errorf("read job status: %w", err)
		}
		if !found {
			return entity.JobStatus{}, errors.New("job has no live worker")
		}
		switch status.State {
		case entity.JobStateCompleted:
			return status, nil
		case entity.JobStateFailed:
			return entity.JobStatus{}, jobStatusError(status)
		}
		select {
		case <-ctx.Done():
			return entity.JobStatus{}, context.Cause(ctx)
		case <-ticker.C:
		}
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

var errJobNotFound = tangoerrors.NewUser(errors.New("job not found"))
