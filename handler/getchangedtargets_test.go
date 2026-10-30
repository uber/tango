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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/config"
	mock_controller "github.com/uber/tango/controller/controllermock"
	"github.com/uber/tango/entity"
	pb "github.com/uber/tango/tangopb"
	tangomock "github.com/uber/tango/tangopb/tangopbmock"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap/zaptest"
)

func TestGetChangedTargetsForwardsToController(t *testing.T) {
	gc := gomock.NewController(t)
	ctrl := mock_controller.NewMockController(gc)
	h := New(Params{Logger: zaptest.NewLogger(t), RepoConfig: allowAnyRepositoryConfigProvider{}, Controller: ctrl})

	req := &pb.GetChangedTargetsRequest{
		FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
		OutputConfig:   &pb.OutputConfig{MaxDistance: 2},
		BypassCache:    true,
	}
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(gc)
	stream.EXPECT().Context().Return(t.Context())
	stream.EXPECT().Send(gomock.Any()).Return(nil)
	ctrl.EXPECT().GetChangedTargets(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, gotReq entity.GetChangedTargetsRequest, gotRepo config.RepositoryConfig) ([]entity.GetChangedTargetsResponse, error) {
			assert.Equal(t, "sha1", gotReq.First.BaseSha)
			assert.Equal(t, "sha2", gotReq.Second.BaseSha)
			assert.True(t, gotReq.BypassCache)
			assert.Equal(t, "test-repository", gotRepo.RepositoryID)
			return []entity.GetChangedTargetsResponse{{ChangedTargets: []entity.ChangedTarget{}}}, nil
		})

	require.NoError(t, h.GetChangedTargets(req, stream))
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
