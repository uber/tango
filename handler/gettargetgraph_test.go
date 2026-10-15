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
	"github.com/uber/tango/config"
	mock_controller "github.com/uber/tango/controller/controllermock"
	"github.com/uber/tango/entity"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap/zaptest"
)

type fakeGetTargetGraphStream struct {
	pb.TangoServiceGetTargetGraphYARPCServer
}

func TestGetTargetGraphForwardsToController(t *testing.T) {
	ctrl := mock_controller.NewMockController(gomock.NewController(t))
	h := New(Params{Logger: zaptest.NewLogger(t), RepoConfig: allowAnyRepositoryConfigProvider{}, Controller: ctrl})

	req := &pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha"},
		OutputConfig:     &pb.OutputConfig{IncludeHashes: true},
	}
	stream := &fakeGetTargetGraphStream{}
	ctrl.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(gotReq entity.GetTargetGraphRequest, gotOutput *pb.OutputConfig, gotStream pb.TangoServiceGetTargetGraphYARPCServer, gotRepo config.RepositoryConfig) error {
			assert.Equal(t, "sha", gotReq.Build.BaseSha)
			assert.Same(t, req.OutputConfig, gotOutput)
			assert.Same(t, stream, gotStream)
			assert.Equal(t, "test-repository", gotRepo.RepositoryID)
			return nil
		})

	require.NoError(t, h.GetTargetGraph(req, stream))
}
