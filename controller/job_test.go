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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uber/tango/config"
	"github.com/uber/tango/core/cachekey"
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/orchestrator"
	orchestratormock "github.com/uber/tango/orchestrator/orchestratormock"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func jobTestRepository() config.RepositoryConfig {
	return config.RepositoryConfig{Remote: "repo:go-code", RepositoryID: testRepositoryID("repo:go-code")}
}

func targetGraphJobEntityRequest() entity.GetTargetGraphRequest {
	return entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{Strategy: entity.ComputationStrategyUnset, Remote: "repo:go-code", BaseSha: "sha1"},
	}
}

func targetGraphJobKey(request entity.GetTargetGraphRequest) string {
	return cachekey.GetTargetGraphJobPath(cachekey.GetTargetGraphJobToken(request))
}

func changedTargetsJobKey(request entity.GetChangedTargetsRequest) string {
	return cachekey.GetChangedTargetsJobPath(cachekey.GetChangedTargetsJobToken(request.First, request.Second, request.ExcludeFilesRegex, request.BypassCache))
}

func newJobTestController(t *testing.T, appCtx context.Context, st storage.Storage, orch orchestrator.Orchestrator) *controller {
	t.Helper()
	return NewController(appCtx, Params{
		Logger:               zaptest.NewLogger(t),
		Storage:              st,
		Orchestrator:         orch,
		JobHeartbeatInterval: 100 * time.Millisecond,
	}).(*controller)
}

// expectGraphCompute emulates the orchestrator, which writes the treehash and graph before it returns.
func expectGraphCompute(t *testing.T, orch *orchestratormock.MockOrchestrator, st storage.Storage, chunksBySha map[string][]entity.GetTargetGraphResponse, errBySha map[string]error) {
	t.Helper()
	repositoryID := testRepositoryID("repo:go-code")
	orch.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, req entity.GetTargetGraphRequest) (storage.GraphReader, error) {
			if err := errBySha[req.Build.BaseSha]; err != nil {
				return nil, err
			}
			treehash := "treehash-" + req.Build.BaseSha
			chunks := chunksBySha[req.Build.BaseSha]
			require.NoError(t, st.Put(ctx, storage.UploadRequest{
				Key:    cachekey.GetTreehashCachePath(repositoryID, req.Build),
				Reader: strings.NewReader(treehash),
			}))
			require.NoError(t, storage.WriteGraphStream(ctx, st,
				cachekey.GetGraphByTreeHash(repositoryID, treehash, req.Build.Strategy, req.ExcludeFilesRegex), chunks))
			return newGraphReader(t, chunks...), nil
		}).Times(len(chunksBySha) + len(errBySha))
}

func waitForJobState(t *testing.T, st storage.Storage, key string, state entity.JobState) entity.JobStatus {
	t.Helper()
	var got entity.JobStatus
	require.Eventually(t, func() bool {
		status, found, err := storage.ReadJobStatus(t.Context(), st, key, time.Minute)
		got = status
		return err == nil && found && status.State == state
	}, 5*time.Second, 10*time.Millisecond, "job did not reach %s", state)
	return got
}

func TestSubmitJob_ActiveJobReturnsExistingStatus(t *testing.T) {
	// No EXPECT: an active job must not start a new computation.
	t.Run("target graph", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		st := storage.NewMemoryStorage()
		running := entity.JobStatus{State: entity.JobStateRunning, LastUpdated: time.Now()}
		require.NoError(t, storage.WriteJobStatus(t.Context(), st, targetGraphJobKey(targetGraphJobEntityRequest()), running))

		c := newJobTestController(t, context.Background(), st, orchestratormock.NewMockOrchestrator(ctrl))
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		token, status, err := c.SubmitTargetGraphJob(ctx, targetGraphJobEntityRequest(), jobTestRepository())
		require.NoError(t, err)
		assert.Equal(t, cachekey.GetTargetGraphJobToken(targetGraphJobEntityRequest()), token)
		assert.Equal(t, entity.JobStateRunning, status.State)
	})

	t.Run("changed targets", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		st := storage.NewMemoryStorage()
		request := changedTargetsRequest()
		running := entity.JobStatus{State: entity.JobStateRunning, LastUpdated: time.Now()}
		require.NoError(t, storage.WriteJobStatus(t.Context(), st, changedTargetsJobKey(request), running))

		c := newJobTestController(t, context.Background(), st, orchestratormock.NewMockOrchestrator(ctrl))
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, status, submitErr := c.SubmitChangedTargetsJob(ctx, request, jobTestRepository())
		require.NoError(t, submitErr)
		assert.Equal(t, entity.JobStateRunning, status.State)
	})
}

func TestSubmitTargetGraphJob_FinishedJob(t *testing.T) {
	bypass := targetGraphJobEntityRequest()
	bypass.BypassCache = true
	cases := []struct {
		name         string
		request      entity.GetTargetGraphRequest
		status       entity.JobStatus
		resultExists bool
		want         entity.JobState
	}{
		{name: "completed with result returns COMPLETED", request: targetGraphJobEntityRequest(), status: entity.JobStatus{State: entity.JobStateCompleted}, resultExists: true, want: entity.JobStateCompleted},
		{name: "completed without result starts a job", request: targetGraphJobEntityRequest(), status: entity.JobStatus{State: entity.JobStateCompleted}, want: entity.JobStateRunning},
		{name: "completed bypass_cache starts a job", request: bypass, status: entity.JobStatus{State: entity.JobStateCompleted}, resultExists: true, want: entity.JobStateRunning},
		{name: "failed starts a job", request: targetGraphJobEntityRequest(), status: entity.JobStatus{State: entity.JobStateFailed}, want: entity.JobStateRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			st := storage.NewMemoryStorage()
			orch := orchestratormock.NewMockOrchestrator(ctrl)
			orch.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any()).Return(nil, errors.New("compute failed")).AnyTimes()
			key := targetGraphJobKey(tc.request)
			tc.status.ResultKey = "result"
			require.NoError(t, storage.WriteJobStatus(t.Context(), st, key, tc.status))
			if tc.resultExists {
				require.NoError(t, st.Put(t.Context(), storage.UploadRequest{Key: "result", Reader: strings.NewReader("graph")}))
			}

			c := newJobTestController(t, context.Background(), st, orch)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			_, status, err := c.SubmitTargetGraphJob(ctx, tc.request, jobTestRepository())
			require.NoError(t, err)
			assert.Equal(t, tc.want, status.State)
			if tc.want == entity.JobStateRunning {
				waitForJobState(t, st, key, entity.JobStateFailed)
			}
		})
	}
}

func TestSubmitTargetGraphJob_ComputesAndPollCompletes(t *testing.T) {
	ctrl := gomock.NewController(t)
	st := storage.NewMemoryStorage()
	orch := orchestratormock.NewMockOrchestrator(ctrl)
	expectGraphCompute(t, orch, st, map[string][]entity.GetTargetGraphResponse{"sha1": {
		{Targets: []entity.OptimizedTarget{{ID: 1}}},
		{Targets: []entity.OptimizedTarget{{ID: 2}}},
		{Metadata: &entity.Metadata{TargetIDMapping: map[int32]string{1: "//a"}}},
		{Metadata: &entity.Metadata{TargetIDMapping: map[int32]string{2: "//b"}}},
	}}, nil)

	c := newJobTestController(t, context.Background(), st, orch)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	token, status, err := c.SubmitTargetGraphJob(ctx, targetGraphJobEntityRequest(), jobTestRepository())
	require.NoError(t, err)
	assert.Equal(t, entity.JobStateRunning, status.State)

	got := waitForJobState(t, st, cachekey.GetTargetGraphJobPath(token), entity.JobStateCompleted)
	assert.Equal(t, testRepositoryID("repo:go-code"), got.RepositoryID)

	// Poll reads the result key from the job record, not through the treehash mapping.
	require.NoError(t, st.Put(t.Context(), storage.UploadRequest{
		Key:    cachekey.GetTreehashCachePath(testRepositoryID("repo:go-code"), targetGraphJobEntityRequest().Build),
		Reader: strings.NewReader("other-treehash"),
	}))
	status, graphReader, err := c.PollTargetGraphJob(t.Context(), token)
	require.NoError(t, err)
	assert.Equal(t, testRepositoryID("repo:go-code"), status.RepositoryID)
	assert.Equal(t, entity.JobStateCompleted, status.State)
	assert.Len(t, drainGraphReader(t, graphReader), 4)
}

func TestSubmitChangedTargetsJob_WaitsForGraphJobs(t *testing.T) {
	t.Run("completes", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		st := storage.NewMemoryStorage()
		orch := orchestratormock.NewMockOrchestrator(ctrl)
		expectGraphCompute(t, orch, st, map[string][]entity.GetTargetGraphResponse{
			"sha1": tgbTestGraphChunks("hash-a"),
			"sha2": tgbTestGraphChunks("hash-b"),
		}, nil)

		c := newJobTestController(t, context.Background(), st, orch)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		request := changedTargetsRequest()
		token, _, err := c.SubmitChangedTargetsJob(ctx, request, jobTestRepository())
		require.NoError(t, err)

		waitForJobState(t, st, cachekey.GetChangedTargetsJobPath(token), entity.JobStateCompleted)
		waitForJobState(t, st, targetGraphJobKey(entity.GetTargetGraphRequest{Build: request.First}), entity.JobStateCompleted)
		waitForJobState(t, st, targetGraphJobKey(entity.GetTargetGraphRequest{Build: request.Second}), entity.JobStateCompleted)

		status, chunks, err := c.PollChangedTargetsJob(t.Context(), token)
		require.NoError(t, err)
		assert.Equal(t, entity.JobStateCompleted, status.State)
		assert.NotEmpty(t, chunks)
	})

	t.Run("reuses a completed graph job", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		st := storage.NewMemoryStorage()
		orch := orchestratormock.NewMockOrchestrator(ctrl)
		expectGraphCompute(t, orch, st, map[string][]entity.GetTargetGraphResponse{
			"sha1": tgbTestGraphChunks("hash-a"),
			"sha2": tgbTestGraphChunks("hash-b"),
		}, nil)

		c := newJobTestController(t, context.Background(), st, orch)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		request := changedTargetsRequest()
		firstKey := targetGraphJobKey(entity.GetTargetGraphRequest{Build: request.First})
		_, _, err := c.SubmitTargetGraphJob(ctx, entity.GetTargetGraphRequest{Build: request.First}, jobTestRepository())
		require.NoError(t, err)
		before := waitForJobState(t, st, firstKey, entity.JobStateCompleted)

		token, _, err := c.SubmitChangedTargetsJob(ctx, request, jobTestRepository())
		require.NoError(t, err)
		waitForJobState(t, st, cachekey.GetChangedTargetsJobPath(token), entity.JobStateCompleted)

		after := waitForJobState(t, st, firstKey, entity.JobStateCompleted)
		assert.True(t, before.LastUpdated.Equal(after.LastUpdated), "the completed graph job must not run again")
	})

	t.Run("graph job failure fails the job", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		st := storage.NewMemoryStorage()
		orch := orchestratormock.NewMockOrchestrator(ctrl)
		expectGraphCompute(t, orch, st,
			map[string][]entity.GetTargetGraphResponse{"sha1": tgbTestGraphChunks("hash-a")},
			map[string]error{"sha2": tangoerrors.NewUser(errors.New("bad revision"))})

		c := newJobTestController(t, context.Background(), st, orch)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		request := changedTargetsRequest()
		token, _, err := c.SubmitChangedTargetsJob(ctx, request, jobTestRepository())
		require.NoError(t, err)

		got := waitForJobState(t, st, cachekey.GetChangedTargetsJobPath(token), entity.JobStateFailed)
		assert.Equal(t, tangoerrors.ErrorUser, got.ErrorCode)
		waitForJobState(t, st, targetGraphJobKey(entity.GetTargetGraphRequest{Build: request.First}), entity.JobStateCompleted)
	})
}

func TestSubmitTargetGraphJob_ComputeFailureMarksFailed(t *testing.T) {
	ctrl := gomock.NewController(t)
	st := storage.NewMemoryStorage()
	orch := orchestratormock.NewMockOrchestrator(ctrl)
	injected := tangoerrors.NewUser(errors.New("bad request"))
	orch.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any()).Return(nil, injected).Times(1)

	c := newJobTestController(t, context.Background(), st, orch)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, _, err := c.SubmitTargetGraphJob(ctx, targetGraphJobEntityRequest(), jobTestRepository())
	require.NoError(t, err)

	got := waitForJobState(t, st, targetGraphJobKey(targetGraphJobEntityRequest()), entity.JobStateFailed)
	assert.Equal(t, tangoerrors.ErrorUser, got.ErrorCode)
}

func TestPollTargetGraphJob_RunningReturnsStatusOnly(t *testing.T) {
	ctrl := gomock.NewController(t)
	st := storage.NewMemoryStorage()
	require.NoError(t, storage.WriteJobStatus(t.Context(), st, targetGraphJobKey(targetGraphJobEntityRequest()), entity.JobStatus{
		State: entity.JobStateRunning, LastUpdated: time.Now(),
	}))

	c := newJobTestController(t, context.Background(), st, orchestratormock.NewMockOrchestrator(ctrl))
	status, graphReader, err := c.PollTargetGraphJob(t.Context(), cachekey.GetTargetGraphJobToken(targetGraphJobEntityRequest()))
	require.NoError(t, err)
	assert.Equal(t, entity.JobStateRunning, status.State)
	assert.Nil(t, graphReader)
}

func TestPollJob_NotFound(t *testing.T) {
	t.Run("target graph", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		c := newJobTestController(t, context.Background(), storage.NewMemoryStorage(), orchestratormock.NewMockOrchestrator(ctrl))
		_, _, err := c.PollTargetGraphJob(t.Context(), "missing")
		require.Error(t, err)
	})

	t.Run("changed targets", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		c := newJobTestController(t, context.Background(), storage.NewMemoryStorage(), orchestratormock.NewMockOrchestrator(ctrl))
		_, _, err := c.PollChangedTargetsJob(t.Context(), "missing")
		require.Error(t, err)
	})
}

type ctxStorage struct{ storage.Storage }

func (s ctxStorage) Put(ctx context.Context, req storage.UploadRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Storage.Put(ctx, req)
}

func TestSubmitTargetGraphJob_ShutdownMarksFailed(t *testing.T) {
	ctrl := gomock.NewController(t)
	st := ctxStorage{storage.NewMemoryStorage()}
	orch := orchestratormock.NewMockOrchestrator(ctrl)
	appCtx, shutdown := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	// The compute step ignores cancellation, so only the job loop can write FAILED.
	orch.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ entity.GetTargetGraphRequest) (storage.GraphReader, error) {
			close(started)
			<-release
			return nil, ctx.Err()
		}).Times(1)

	c := NewController(appCtx, Params{
		Logger:       zap.NewNop(),
		Storage:      storage.NewMemoryStorage(),
		Orchestrator: orch,
		JobStorage:   st,
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, _, err := c.SubmitTargetGraphJob(ctx, targetGraphJobEntityRequest(), jobTestRepository())
	require.NoError(t, err)
	<-started
	shutdown()

	got := waitForJobState(t, st, targetGraphJobKey(targetGraphJobEntityRequest()), entity.JobStateFailed)
	assert.Equal(t, tangoerrors.ErrorCancelled, got.ErrorCode)
}

func TestSubmitJob_RequiresDeadline(t *testing.T) {
	// No EXPECT: a rejected submit must not start a computation.
	ctrl := gomock.NewController(t)
	c := newJobTestController(t, context.Background(), storage.NewMemoryStorage(), orchestratormock.NewMockOrchestrator(ctrl))

	_, _, err := c.SubmitTargetGraphJob(context.Background(), targetGraphJobEntityRequest(), jobTestRepository())
	require.Error(t, err)
	_, _, err = c.SubmitChangedTargetsJob(context.Background(), changedTargetsRequest(), jobTestRepository())
	require.Error(t, err)
}
