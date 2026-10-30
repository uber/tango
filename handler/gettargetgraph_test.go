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
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/config"
	mock_controller "github.com/uber/tango/controller/controllermock"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
	pb "github.com/uber/tango/tangopb"
	tangomock "github.com/uber/tango/tangopb/tangopbmock"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap/zaptest"
)

type fakeGraphReader struct {
	chunks []entity.GetTargetGraphResponse
	err    error
	closed bool
}

func (f *fakeGraphReader) Read() (entity.GetTargetGraphResponse, error) {
	if f.err != nil {
		return entity.GetTargetGraphResponse{}, f.err
	}
	if len(f.chunks) == 0 {
		return entity.GetTargetGraphResponse{}, io.EOF
	}
	c := f.chunks[0]
	f.chunks = f.chunks[1:]
	return c, nil
}

func (f *fakeGraphReader) Close() error {
	f.closed = true
	return nil
}

func targetGraphRequest() *pb.GetTargetGraphRequest {
	return &pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha"},
	}
}

func TestGetTargetGraphForwardsToController(t *testing.T) {
	gc := gomock.NewController(t)
	ctrl := mock_controller.NewMockController(gc)
	h := New(Params{Logger: zaptest.NewLogger(t), RepoConfig: allowAnyRepositoryConfigProvider{}, Controller: ctrl})

	req := targetGraphRequest()
	req.OutputConfig = &pb.OutputConfig{IncludeHashes: true}
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(gc)
	stream.EXPECT().Context().Return(t.Context())
	var sent []*pb.GetTargetGraphResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(r *pb.GetTargetGraphResponse, _ ...any) error {
		sent = append(sent, r)
		return nil
	})
	reader := &fakeGraphReader{chunks: []entity.GetTargetGraphResponse{{Targets: []entity.OptimizedTarget{{ID: 1, Hash: "h1"}}}}}
	ctrl.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, gotReq entity.GetTargetGraphRequest, gotRepo config.RepositoryConfig) (storage.GraphReader, error) {
			assert.Equal(t, "sha", gotReq.Build.BaseSha)
			assert.Equal(t, "test-repository", gotRepo.RepositoryID)
			return reader, nil
		})

	require.NoError(t, h.GetTargetGraph(req, stream))
	require.Len(t, sent, 1)
	assert.Equal(t, "h1", sent[0].GetTargets().GetTargets()[0].GetHash())
	assert.True(t, reader.closed)
}

func TestGetTargetGraph_GraphReadError(t *testing.T) {
	gc := gomock.NewController(t)
	ctrl := mock_controller.NewMockController(gc)
	h := New(Params{Logger: zaptest.NewLogger(t), RepoConfig: allowAnyRepositoryConfigProvider{}, Controller: ctrl})
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(gc)
	stream.EXPECT().Context().Return(t.Context())
	reader := &fakeGraphReader{err: errors.New("readfail")}
	ctrl.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any(), gomock.Any()).Return(reader, nil)

	assert.Error(t, h.GetTargetGraph(targetGraphRequest(), stream))
	assert.True(t, reader.closed)
}

func TestGetTargetGraph_StreamSendError(t *testing.T) {
	gc := gomock.NewController(t)
	ctrl := mock_controller.NewMockController(gc)
	h := New(Params{Logger: zaptest.NewLogger(t), RepoConfig: allowAnyRepositoryConfigProvider{}, Controller: ctrl})
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(gc)
	stream.EXPECT().Context().Return(t.Context())
	stream.EXPECT().Send(gomock.Any()).Return(errors.New("send fail"))
	reader := &fakeGraphReader{chunks: []entity.GetTargetGraphResponse{{Targets: []entity.OptimizedTarget{}}}}
	ctrl.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any(), gomock.Any()).Return(reader, nil)

	assert.Error(t, h.GetTargetGraph(targetGraphRequest(), stream))
	assert.True(t, reader.closed)
}
