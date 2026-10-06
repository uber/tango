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
package handler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mock_controller "github.com/uber/tango/controller/controllermock"
	"github.com/uber/tango/entity"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap/zaptest"
)

func TestPollTargetGraphJobMergesChunks(t *testing.T) {
	gc := gomock.NewController(t)
	ctrl := mock_controller.NewMockController(gc)
	h := New(Params{Logger: zaptest.NewLogger(t), RepoConfig: allowAnyRepositoryConfigProvider{}, Controller: ctrl})

	reader := &fakeGraphReader{chunks: []entity.GetTargetGraphResponse{
		{Targets: []entity.OptimizedTarget{{ID: 1}}},
		{Targets: []entity.OptimizedTarget{{ID: 2}}},
		{Metadata: &entity.Metadata{TargetIDMapping: map[int32]string{1: "//a"}}},
		{Metadata: &entity.Metadata{TargetIDMapping: map[int32]string{2: "//b"}}},
	}}
	ctrl.EXPECT().PollTargetGraphJob(gomock.Any(), "token").
		Return(entity.JobStatus{State: entity.JobStateCompleted, RepositoryID: "repo"}, reader, nil)

	resp, err := h.PollTargetGraphJob(t.Context(), &pb.PollJobRequest{Token: "token"})
	require.NoError(t, err)
	assert.Equal(t, pb.JOB_STATE_COMPLETED, resp.GetStatus().GetState())
	assert.Len(t, resp.GetTargets().GetTargets(), 2)
	assert.Len(t, resp.GetMetadata().GetTargetIdMapping(), 2)
	assert.True(t, reader.closed)
}
