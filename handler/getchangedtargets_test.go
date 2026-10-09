// Copyright (c) 2025 Uber Technologies, Inc.
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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mock_controller "github.com/uber/tango/controller/controllermock"
	"github.com/uber/tango/entity"
	pb "github.com/uber/tango/tangopb"
	tangomock "github.com/uber/tango/tangopb/tangopbmock"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap/zaptest"
)

func changedTargetsRequest() *pb.GetChangedTargetsRequest {
	return &pb.GetChangedTargetsRequest{
		FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
		OutputConfig:   &pb.OutputConfig{MaxDistance: -1},
	}
}

func TestGetChangedTargets_ValidationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)

	h := New(Params{Logger: zaptest.NewLogger(t), RepoConfig: allowAnyRepositoryConfigProvider{}, Controller: mock_controller.NewMockController(ctrl)})

	err := h.GetChangedTargets(nil, stream)
	require.Error(t, err)
}

func TestGetChangedTargets_streamChunks(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())

	var sentResponses []*pb.GetChangedTargetsResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(resp *pb.GetChangedTargetsResponse, opts ...interface{}) error {
		sentResponses = append(sentResponses, resp)
		return nil
	}).Times(2)

	result := entity.ChangedTargetsResult{
		ChangedTargets: []entity.ChangedTarget{
			{
				ChangeType: entity.ChangeTypeChanged,
				OldTarget:  &entity.OptimizedTarget{ID: 1, Hash: "h2-old"},
				NewTarget:  &entity.OptimizedTarget{ID: 1, Hash: "h2-new"},
				Distance:   0,
			},
		},
		Metadata: &entity.Metadata{
			TargetIDMapping: map[int32]string{1: "//app:target2"},
		},
	}

	mockController := mock_controller.NewMockController(ctrl)
	mockController.EXPECT().GetChangedTargets(gomock.Any(), gomock.Any(), gomock.Any()).Return(result, nil)

	h := New(Params{Logger: zaptest.NewLogger(t), RepoConfig: allowAnyRepositoryConfigProvider{}, Controller: mockController})

	request := changedTargetsRequest()
	request.OutputConfig = &pb.OutputConfig{MaxDistance: -1, IncludeHashes: true, IncludeTags: true, IncludeAttributes: true}

	err := h.GetChangedTargets(request, stream)
	require.NoError(t, err)

	require.Len(t, sentResponses, 2)
	changedTargets := sentResponses[0].GetChangedTargets()
	metadata := sentResponses[1].GetMetadata()

	require.Len(t, changedTargets.GetChangedTargets(), 1)
	changed := changedTargets.GetChangedTargets()[0]
	assert.Equal(t, "h2-old", changed.GetOldTarget().GetHash())
	assert.Equal(t, "h2-new", changed.GetNewTarget().GetHash())

	targetID := changed.GetNewTarget().GetId()
	assert.Equal(t, "//app:target2", metadata.GetTargetIdMapping()[targetID])
}

func TestGetChangedTargets_StreamSendError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())
	stream.EXPECT().Send(gomock.Any()).Return(errors.New("send error"))

	mockController := mock_controller.NewMockController(ctrl)
	mockController.EXPECT().GetChangedTargets(gomock.Any(), gomock.Any(), gomock.Any()).Return(
		entity.ChangedTargetsResult{ChangedTargets: []entity.ChangedTarget{}}, nil)

	h := New(Params{Logger: zaptest.NewLogger(t), RepoConfig: allowAnyRepositoryConfigProvider{}, Controller: mockController})

	err := h.GetChangedTargets(changedTargetsRequest(), stream)
	assert.Error(t, err)
}

func TestGetChangedTargets_ControllerError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())

	injected := errors.New("storage exploded")
	mockController := mock_controller.NewMockController(ctrl)
	mockController.EXPECT().GetChangedTargets(gomock.Any(), gomock.Any(), gomock.Any()).Return(entity.ChangedTargetsResult{}, injected)

	h := New(Params{Logger: zaptest.NewLogger(t), RepoConfig: allowAnyRepositoryConfigProvider{}, Controller: mockController})

	err := h.GetChangedTargets(changedTargetsRequest(), stream)
	require.Error(t, err)
	assert.Equal(t, pb.ERROR_INFRA, wireErrorCode(t, err))
}

func TestSendTrimmedChangedTargets_MetadataAlwaysForwarded(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)

	responses := []entity.GetChangedTargetsResponse{
		{
			ChangedTargets: []entity.ChangedTarget{
				{Distance: 5, ChangeType: entity.ChangeTypeChanged},
			},
		},
		{
			Metadata: &entity.Metadata{TargetIDMapping: map[int32]string{1: "//app:T"}},
		},
	}

	var sent []*pb.GetChangedTargetsResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(r *pb.GetChangedTargetsResponse, _ ...any) error {
		sent = append(sent, r)
		return nil
	}).Times(2)

	// max_distance=1 filters out the distance-5 target, metadata always forwarded
	require.NoError(t, sendTrimmedChangedTargets(stream, responses, 1, nil))

	// First response: target filtered out (distance 5 > maxDist 1)
	assert.Empty(t, sent[0].GetChangedTargets().GetChangedTargets())
	// Second response: metadata always forwarded
	assert.NotNil(t, sent[1].GetMetadata())
}

func TestSendTrimmedChangedTargets_SendError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)

	responses := []entity.GetChangedTargetsResponse{
		{
			ChangedTargets: []entity.ChangedTarget{},
		},
	}

	sendErr := errors.New("send error")
	stream.EXPECT().Send(gomock.Any()).Return(sendErr)

	err := sendTrimmedChangedTargets(stream, responses, -1, nil)
	assert.ErrorIs(t, err, sendErr)
}

func TestGetChangedTargets_DistanceFilter(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())

	result := entity.ChangedTargetsResult{
		ChangedTargets: []entity.ChangedTarget{
			{Distance: 0, ChangeType: entity.ChangeTypeChanged},
			{Distance: 2, ChangeType: entity.ChangeTypeChanged},
		},
		Metadata: &entity.Metadata{},
	}

	mockController := mock_controller.NewMockController(ctrl)
	mockController.EXPECT().GetChangedTargets(gomock.Any(), gomock.Any(), gomock.Any()).Return(result, nil)

	var sent []*pb.GetChangedTargetsResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(r *pb.GetChangedTargetsResponse, _ ...any) error {
		sent = append(sent, r)
		return nil
	}).Times(2)

	h := New(Params{Logger: zaptest.NewLogger(t), RepoConfig: allowAnyRepositoryConfigProvider{}, Controller: mockController})

	request := changedTargetsRequest()
	request.OutputConfig = &pb.OutputConfig{MaxDistance: 1}

	err := h.GetChangedTargets(request, stream)
	require.NoError(t, err)

	require.Len(t, sent, 2)
	kept := sent[0].GetChangedTargets().GetChangedTargets()
	require.Len(t, kept, 1, "only the distance-0 target should survive the filter")
	assert.Equal(t, int32(0), kept[0].GetDistance())
	// Metadata always forwarded
	assert.NotNil(t, sent[1].GetMetadata())
}

func TestSendTrimmedChangedTargets_RetainsDeletedAtMaxDistanceOne(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)

	// DELETED entries are seeds (distance 0) and must survive max_distance=1.
	responses := []entity.GetChangedTargetsResponse{
		{
			ChangedTargets: []entity.ChangedTarget{
				{Distance: 0, ChangeType: entity.ChangeTypeDeleted},
				{Distance: 1, ChangeType: entity.ChangeTypeChanged},
				{Distance: 5, ChangeType: entity.ChangeTypeChanged},
			},
		},
	}

	var sent []*pb.GetChangedTargetsResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(r *pb.GetChangedTargetsResponse, _ ...any) error {
		sent = append(sent, r)
		return nil
	}).Times(1)

	require.NoError(t, sendTrimmedChangedTargets(stream, responses, 1, nil))

	kept := sent[0].GetChangedTargets().GetChangedTargets()
	require.Len(t, kept, 2, "distance-0 DELETED and distance-1 CHANGED both kept; distance-5 dropped")
	gotDeleted := false
	for _, ct := range kept {
		if ct.GetChangeType() == pb.CHANGE_TYPE_DELETED {
			gotDeleted = true
			assert.Equal(t, int32(0), ct.GetDistance())
		}
	}
	assert.True(t, gotDeleted, "DELETED entry at distance 0 must survive max_distance=1")
}
