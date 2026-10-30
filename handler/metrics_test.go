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
	"github.com/uber-go/tally"
	mock_controller "github.com/uber/tango/controller/controllermock"
	pb "github.com/uber/tango/tangopb"
	tangomock "github.com/uber/tango/tangopb/tangopbmock"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
)

func TestGetTargetGraphMetrics(t *testing.T) {
	ts := tally.NewTestScope("", nil)
	gc := gomock.NewController(t)
	ctrl := mock_controller.NewMockController(gc)
	ctrl.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
	h := New(Params{Logger: zap.NewNop(), Controller: ctrl, RepoConfig: allowAnyRepositoryConfigProvider{}, Scope: ts})
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(gc)
	stream.EXPECT().Context().Return(t.Context())

	build := &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha", Strategy: pb.COMPUTATION_STRATEGY_UNSET}
	require.NoError(t, h.GetTargetGraph(&pb.GetTargetGraphRequest{BuildDescription: build}, stream))

	snap := ts.Snapshot()
	assert.Contains(t, snap.Counters(), "handler.get_target_graph.start+repo=test-repository")
	assert.Contains(t, snap.Histograms(), "handler.get_target_graph.finish+repo=test-repository,result=success")
}

func TestGetChangedTargetsMetrics(t *testing.T) {
	ts := tally.NewTestScope("", nil)
	ctrl := mock_controller.NewMockController(gomock.NewController(t))
	ctrl.EXPECT().GetChangedTargets(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	h := New(Params{Logger: zap.NewNop(), Controller: ctrl, RepoConfig: allowAnyRepositoryConfigProvider{}, Scope: ts})

	build := &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha", Strategy: pb.COMPUTATION_STRATEGY_UNSET}
	require.NoError(t, h.GetChangedTargets(&pb.GetChangedTargetsRequest{FirstRevision: build, SecondRevision: build}, nil))

	snap := ts.Snapshot()
	assert.Contains(t, snap.Counters(), "handler.get_changed_targets.start+repo=test-repository")
	assert.Contains(t, snap.Histograms(), "handler.get_changed_targets.finish+repo=test-repository,result=success")
}

func TestGetChangedTargetGraphMetrics(t *testing.T) {
	ts := tally.NewTestScope("", nil)
	h := New(Params{Logger: zap.NewNop(), Scope: ts})

	require.Error(t, h.GetChangedTargetGraph(nil, nil))

	snap := ts.Snapshot()
	assert.Contains(t, snap.Counters(), "handler.get_changed_target_graph.start+")
	assert.Contains(t, snap.Histograms(), "handler.get_changed_target_graph.finish+result=infra")
}
