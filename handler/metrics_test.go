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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber-go/tally"
	"github.com/uber/tango/config"
	mock_controller "github.com/uber/tango/controller/controllermock"
	pb "github.com/uber/tango/tangopb"
	tangomock "github.com/uber/tango/tangopb/tangopbmock"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
)

type noRepositoryConfigProvider struct{}

func (noRepositoryConfigProvider) GetRepositoryConfig(string) (config.RepositoryConfig, bool) {
	return config.RepositoryConfig{}, false
}

func TestLifecycleMetrics(t *testing.T) {
	validBuild := &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha", Strategy: pb.COMPUTATION_STRATEGY_UNSET}
	tests := []struct {
		name       string
		op         string
		repoConfig config.RepositoryConfigProvider
		call       func(h pb.TangoYARPCServer, ctrl *gomock.Controller) error
		expect     func(m *mock_controller.MockController)
		wantErr    bool
		wantRepo   string
		wantResult string
	}{
		{
			name:       "GetTargetGraph success carries the configured repository",
			op:         "get_target_graph",
			repoConfig: allowAnyRepositoryConfigProvider{},
			call: func(h pb.TangoYARPCServer, ctrl *gomock.Controller) error {
				return h.GetTargetGraph(&pb.GetTargetGraphRequest{BuildDescription: validBuild}, targetGraphStream(ctrl))
			},
			expect: func(m *mock_controller.MockController) {
				m.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
			},
			wantRepo:   "test-repository",
			wantResult: "success",
		},
		{
			name:       "GetTargetGraph invalid request carries the unknown repository",
			op:         "get_target_graph",
			repoConfig: allowAnyRepositoryConfigProvider{},
			call: func(h pb.TangoYARPCServer, ctrl *gomock.Controller) error {
				return h.GetTargetGraph(&pb.GetTargetGraphRequest{}, targetGraphStream(ctrl))
			},
			wantErr:    true,
			wantRepo:   unknownRepositoryMetricLabel,
			wantResult: "user",
		},
		{
			name:       "GetTargetGraph unconfigured remote does not call the controller",
			op:         "get_target_graph",
			repoConfig: noRepositoryConfigProvider{},
			call: func(h pb.TangoYARPCServer, ctrl *gomock.Controller) error {
				return h.GetTargetGraph(&pb.GetTargetGraphRequest{BuildDescription: validBuild}, targetGraphStream(ctrl))
			},
			wantErr:    true,
			wantRepo:   unknownRepositoryMetricLabel,
			wantResult: "user",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := tally.NewTestScope("", nil)
			gc := gomock.NewController(t)
			ctrl := mock_controller.NewMockController(gc)
			if tt.expect != nil {
				tt.expect(ctrl)
			}
			h := New(Params{Logger: zap.NewNop(), Scope: ts, Controller: ctrl, RepoConfig: tt.repoConfig})

			err := tt.call(h, gc)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			snap := ts.Snapshot()
			prefix := "handler." + tt.op
			assert.Contains(t, snap.Counters(), prefix+".start+repo="+tt.wantRepo)
			assert.Contains(t, snap.Histograms(), prefix+".finish+repo="+tt.wantRepo+",result="+tt.wantResult)
		})
	}
}

func targetGraphStream(ctrl *gomock.Controller) pb.TangoServiceGetTargetGraphYARPCServer {
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background()).AnyTimes()
	return stream
}

func TestGetChangedTargetGraphMetrics(t *testing.T) {
	ts := tally.NewTestScope("", nil)
	h := New(Params{Logger: zap.NewNop(), Scope: ts})

	require.Error(t, h.GetChangedTargetGraph(nil, nil))

	snap := ts.Snapshot()
	assert.Contains(t, snap.Counters(), "handler.get_changed_target_graph.start+")
	assert.Contains(t, snap.Histograms(), "handler.get_changed_target_graph.finish+result=infra")
}
