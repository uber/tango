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
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mock_controller "github.com/uber/tango/controller/controllermock"
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/entity"
	pb "github.com/uber/tango/tangopb"
	tangomock "github.com/uber/tango/tangopb/tangopbmock"
	"go.uber.org/mock/gomock"
	"go.uber.org/yarpc/encoding/protobuf"
	"go.uber.org/zap/zaptest"
)

type fakeGraphReader struct {
	chunks []entity.GetTargetGraphResponse
	i      int
	closed bool
}

func (f *fakeGraphReader) Read() (entity.GetTargetGraphResponse, error) {
	if f.i >= len(f.chunks) {
		return entity.GetTargetGraphResponse{}, io.EOF
	}
	c := f.chunks[f.i]
	f.i++
	return c, nil
}

func (f *fakeGraphReader) Close() error {
	f.closed = true
	return nil
}

func targetGraphRequest() *pb.GetTargetGraphRequest {
	return &pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{
			Strategy: pb.COMPUTATION_STRATEGY_UNSET,
			Remote:   "repo:go-code",
			BaseSha:  "sha",
		},
	}
}

func newTargetGraphHandler(t *testing.T, ctrl *gomock.Controller) (pb.TangoYARPCServer, *mock_controller.MockController) {
	mockController := mock_controller.NewMockController(ctrl)
	h := New(Params{Logger: zaptest.NewLogger(t), RepoConfig: allowAnyRepositoryConfigProvider{}, Controller: mockController})
	return h, mockController
}

func TestGetTargetGraph_NilReader_NoSend(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())
	h, mockController := newTargetGraphHandler(t, ctrl)
	mockController.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)

	require.NoError(t, h.GetTargetGraph(targetGraphRequest(), stream))
}

func TestGetTargetGraph_SendsWhenItemPresent(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())
	stream.EXPECT().Send(gomock.Any()).Return(nil)
	h, mockController := newTargetGraphHandler(t, ctrl)
	reader := &fakeGraphReader{chunks: []entity.GetTargetGraphResponse{{Targets: []entity.OptimizedTarget{}}}}
	mockController.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any(), gomock.Any()).Return(reader, nil)

	require.NoError(t, h.GetTargetGraph(targetGraphRequest(), stream))
	assert.True(t, reader.closed, "the handler must close the reader when done streaming")
}

func TestGetTargetGraph_AppliesOutputConfig(t *testing.T) {
	tests := []struct {
		name         string
		outputConfig *pb.OutputConfig
		wantHash     string
	}{
		{name: "nil config strips hashes", wantHash: ""},
		{name: "include hashes keeps hashes", outputConfig: &pb.OutputConfig{IncludeHashes: true}, wantHash: "h1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
			stream.EXPECT().Context().Return(t.Context())
			var sent []*pb.GetTargetGraphResponse
			stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(r *pb.GetTargetGraphResponse, _ ...any) error {
				sent = append(sent, r)
				return nil
			})
			h, mockController := newTargetGraphHandler(t, ctrl)
			reader := &fakeGraphReader{chunks: []entity.GetTargetGraphResponse{{Targets: []entity.OptimizedTarget{{ID: 1, Hash: "h1"}}}}}
			mockController.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any(), gomock.Any()).Return(reader, nil)

			req := targetGraphRequest()
			req.OutputConfig = tt.outputConfig
			require.NoError(t, h.GetTargetGraph(req, stream))
			require.Len(t, sent, 1)
			assert.Equal(t, tt.wantHash, sent[0].GetTargets().GetTargets()[0].GetHash())
		})
	}
}

func TestGetTargetGraph_InvalidRequest_ReturnsUserError(t *testing.T) {
	tests := []struct {
		name    string
		request *pb.GetTargetGraphRequest
	}{
		{name: "missing build description", request: &pb.GetTargetGraphRequest{}},
		{
			name: "build description missing base sha",
			request: &pb.GetTargetGraphRequest{
				BuildDescription: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
			h, _ := newTargetGraphHandler(t, ctrl)

			err := h.GetTargetGraph(tt.request, stream)
			require.Error(t, err)
			assert.Equal(t, pb.ERROR_USER, wireErrorCode(t, err))
		})
	}
}

func TestGetTargetGraph_ControllerError_Propagates(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())
	h, mockController := newTargetGraphHandler(t, ctrl)
	mockController.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, tangoerrors.NewUser(errors.New("boom")))

	err := h.GetTargetGraph(targetGraphRequest(), stream)
	require.Error(t, err)
	assert.Equal(t, pb.ERROR_USER, wireErrorCode(t, err))
}

func TestGetTargetGraph_StreamSendError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())
	stream.EXPECT().Send(gomock.Any()).Return(errors.New("send fail"))
	h, mockController := newTargetGraphHandler(t, ctrl)
	reader := &fakeGraphReader{chunks: []entity.GetTargetGraphResponse{{Targets: []entity.OptimizedTarget{}}}}
	mockController.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any(), gomock.Any()).Return(reader, nil)

	require.Error(t, h.GetTargetGraph(targetGraphRequest(), stream))
	assert.True(t, reader.closed, "the handler must close the reader on a send error")
}

func wireErrorCode(t *testing.T, err error) pb.ErrorCode {
	t.Helper()
	details := protobuf.GetErrorDetails(err)
	require.Len(t, details, 1)
	tangoErr, ok := details[0].(*pb.TangoError)
	require.True(t, ok)
	return tangoErr.Code
}
