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

package controller

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	gogio "github.com/gogo/protobuf/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/storage"
	storagemock "github.com/uber/tango/core/storage/storagemock"
	orchestratormock "github.com/uber/tango/orchestrator/orchestratormock"
	pb "github.com/uber/tango/tangopb"
	tangomock "github.com/uber/tango/tangopb/tangopbmock"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap/zaptest"
)

func TestGetTargetGraph_CacheMiss_NoSend(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())
	store := storagemock.NewMockStorage(ctrl)
	// Return a valid treehash, then an empty graph blob (no messages).
	gomock.InOrder(
		store.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(&storage.DownloadResponse{ReadCloser: newMockReadCloser([]byte("treehash-empty"))}, nil),
		store.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(&storage.DownloadResponse{ReadCloser: newMockReadCloser(nil)}, nil),
	)
	c := NewController(Params{
		Logger:  zaptest.NewLogger(t),
		Storage: store,
	})
	req := &pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{
			Remote:  "repo:go-code",
			BaseSha: "sha",
			Requests: []*pb.Request{
				{Url: "github://repo/1", Commit: "abc111"},
				{Url: "github://repo/2", Commit: "abc222"},
			},
		},
	}
	err := c.GetTargetGraph(req, stream)
	require.NoError(t, err)
}

func TestGetTargetGraph_StorageError_Propagates(t *testing.T) {
	expected := errors.New("boom")
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())
	storagemock := storagemock.NewMockStorage(ctrl)
	storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, expected)
	c := NewController(Params{
		Logger:  zaptest.NewLogger(t),
		Storage: storagemock,
	})
	err := c.GetTargetGraph(&pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{
			Remote:  "repo:go-code",
			BaseSha: "sha",
			Requests: []*pb.Request{
				{Url: "github://repo/1", Commit: "abc111"},
				{Url: "github://repo/2", Commit: "abc222"},
			},
		},
	}, stream)
	assert.Error(t, expected, err)
}

func TestGetTargetGraph_DecodeError_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())
	storagemock := storagemock.NewMockStorage(ctrl)
	gomock.InOrder(
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&storage.DownloadResponse{ReadCloser: newMockReadCloser([]byte("treehash-abc"))}, nil),
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&storage.DownloadResponse{ReadCloser: newMockReadCloser([]byte("bad-bytes"))}, nil),
	)
	c := NewController(Params{
		Logger:  zaptest.NewLogger(t),
		Storage: storagemock,
	})
	err := c.GetTargetGraph(&pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{
			Remote:  "repo:go-code",
			BaseSha: "sha",
			Requests: []*pb.Request{
				{Url: "github://repo/1", Commit: "abc111"},
				{Url: "github://repo/2", Commit: "abc222"},
			},
		},
	}, stream)
	assert.Error(t, err)
}

func TestGetTargetGraph_SendsWhenItemPresent(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())
	stream.EXPECT().Send(gomock.Any()).Return(nil)
	store := storagemock.NewMockStorage(ctrl)
	var buf bytes.Buffer
	err := gogio.NewDelimitedWriter(&buf).WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Targets{Targets: &pb.OptimizedTargets{}},
	})
	require.NoError(t, err)

	gomock.InOrder(
		store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&storage.DownloadResponse{ReadCloser: newMockReadCloser([]byte("treehash-xyz"))}, nil),
		store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&storage.DownloadResponse{ReadCloser: newMockReadCloser(buf.Bytes())}, nil),
	)
	c := NewController(Params{
		Logger:  zaptest.NewLogger(t),
		Storage: store,
	})
	err = c.GetTargetGraph(&pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{
			Remote:  "repo:go-code",
			BaseSha: "sha",
			Requests: []*pb.Request{
				{Url: "github://repo/1", Commit: "abc111"},
				{Url: "github://repo/2", Commit: "abc222"},
			},
		},
	}, stream)
	require.NoError(t, err)
}

func TestGetTargetGraph_BuildDescriptionMissingRequiredFields_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())
	store := storagemock.NewMockStorage(ctrl)
	c := NewController(Params{
		Logger:  zaptest.NewLogger(t),
		Storage: store,
	})
	err := c.GetTargetGraph(&pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{
			Remote: "repo:go-code",
			Requests: []*pb.Request{
				{Url: "github://repo/1", Commit: "abc111"},
				{Url: "github://repo/2", Commit: "abc222"},
			},
		},
	}, stream)
	assert.Error(t, err)
}

func TestGetTargetGraph_MissingBuildDescription_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())
	store := storagemock.NewMockStorage(ctrl)
	c := NewController(Params{
		Logger:  zaptest.NewLogger(t),
		Storage: store,
	})
	err := c.GetTargetGraph(&pb.GetTargetGraphRequest{}, stream)
	assert.Error(t, err)
}

// New coverage: Storage returns NotFound on treehash path -> orchestrator is called to compute the target graph.
func TestGetTargetGraph_TreehashNotFound_NoError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())
	stream.EXPECT().Send(gomock.Any()).Return(nil)

	store := storagemock.NewMockStorage(ctrl)
	store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, &storage.NotFoundError{Path: "x"})
	orchestrator := orchestratormock.NewMockOrchestrator(ctrl)
	// Provide a fake GraphReader that yields one message then EOF
	graphReader := storagemock.NewMockGraphReader(ctrl)
	graphReader.EXPECT().Read().DoAndReturn(func() (*pb.GetTargetGraphResponse, error) {
		return &pb.GetTargetGraphResponse{
			Item: &pb.GetTargetGraphResponse_Targets{Targets: &pb.OptimizedTargets{}},
		}, nil
	}).Times(1)
	// Controller may call Read again to observe EOF
	graphReader.EXPECT().Read().Return(nil, io.EOF).Times(1)
	graphReader.EXPECT().Close().Return(nil)
	orchestrator.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any()).Return(graphReader, nil)
	c := NewController(Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      store,
		Orchestrator: orchestrator,
	})
	err := c.GetTargetGraph(&pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha"},
	}, stream)
	require.NoError(t, err)
}

// New coverage: io.ReadAll fails on treehash read -> error returned.
func TestGetTargetGraph_TreehashReadError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())
	store := storagemock.NewMockStorage(ctrl)
	store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&storage.DownloadResponse{ReadCloser: &errReadCloser{err: errors.New("readfail")}}, nil)
	c := NewController(Params{
		Logger:  zaptest.NewLogger(t),
		Storage: store,
	})
	err := c.GetTargetGraph(&pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha"},
	}, stream)
	assert.Error(t, err)
}

// New coverage: graph fetch returns error -> error returned.
func TestGetTargetGraph_GraphFetchError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())
	store := storagemock.NewMockStorage(ctrl)
	gomock.InOrder(
		store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&storage.DownloadResponse{ReadCloser: newMockReadCloser([]byte("treehash-abc"))}, nil),
		store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, errors.New("graph error")),
	)
	c := NewController(Params{
		Logger:  zaptest.NewLogger(t),
		Storage: store,
	})
	err := c.GetTargetGraph(&pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha"},
	}, stream)
	assert.Error(t, err)
}

// New coverage: io.ReadFrom fails on graph read -> error returned.
func TestGetTargetGraph_GraphReadError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())
	store := storagemock.NewMockStorage(ctrl)
	gomock.InOrder(
		store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&storage.DownloadResponse{ReadCloser: newMockReadCloser([]byte("treehash-abc"))}, nil),
		store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&storage.DownloadResponse{ReadCloser: &errReadCloser{err: errors.New("readfail")}}, nil),
	)
	c := NewController(Params{
		Logger:  zaptest.NewLogger(t),
		Storage: store,
	})
	err := c.GetTargetGraph(&pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha"},
	}, stream)
	assert.Error(t, err)
}

// New coverage: stream send error is propagated.
func TestGetTargetGraph_StreamSendError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())
	storagemock := storagemock.NewMockStorage(ctrl)

	var buf bytes.Buffer
	err := gogio.NewDelimitedWriter(&buf).WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Targets{Targets: &pb.OptimizedTargets{}},
	})
	require.NoError(t, err)

	stream.EXPECT().Send(gomock.Any()).Return(errors.New("send fail"))
	gomock.InOrder(
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&storage.DownloadResponse{ReadCloser: newMockReadCloser([]byte("treehash-abc"))}, nil),
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&storage.DownloadResponse{ReadCloser: newMockReadCloser(buf.Bytes())}, nil),
	)
	c := NewController(Params{
		Logger:  zaptest.NewLogger(t),
		Storage: storagemock,
	})
	err = c.GetTargetGraph(&pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha"},
	}, stream)
	assert.Error(t, err)
}

func newMockReadCloser(data []byte) io.ReadCloser {
	if data == nil {
		return nil
	}
	return io.NopCloser(bytes.NewReader(data))
}

type errReadCloser struct{ err error }

func (e *errReadCloser) Read(p []byte) (int, error) { return 0, e.err }
func (e *errReadCloser) Close() error               { return nil }
