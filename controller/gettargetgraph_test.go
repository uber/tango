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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/storage"
	storagemock "github.com/uber/tango/core/storage/storagemock"
	"github.com/uber/tango/entity"
	orchestratormock "github.com/uber/tango/orchestrator/orchestratormock"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap/zaptest"
)

// drainGraphReader reads every chunk off r, asserting no error occurs partway
// through, and returns the chunks it saw.
func drainGraphReader(t *testing.T, r storage.GraphReader) []entity.GetTargetGraphResponse {
	t.Helper()
	if r == nil {
		return nil
	}
	defer func() { _ = r.Close() }()
	var chunks []entity.GetTargetGraphResponse
	for {
		chunk, err := r.Read()
		if err == io.EOF {
			return chunks
		}
		require.NoError(t, err)
		chunks = append(chunks, chunk)
	}
}

func TestGetTargetGraph_CacheHitEmptyGraph(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := storagemock.NewMockStorage(ctrl)
	// Return a valid treehash, then an empty graph blob (no messages).
	gomock.InOrder(
		store.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(storage.DownloadResponse{ReadCloser: newMockReadCloser([]byte("treehash-empty"))}, nil),
		store.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(storage.DownloadResponse{ReadCloser: newMockReadCloser([]byte{})}, nil),
	)
	c := NewController(context.Background(), Params{
		Logger:  zaptest.NewLogger(t),
		Storage: store,
	})
	reader, err := c.GetTargetGraph(t.Context(), entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{Remote: "repo:go-code", BaseSha: "sha"},
	}, testRepositoryConfig)
	require.NoError(t, err)
	assert.Empty(t, drainGraphReader(t, reader))
}

func TestGetTargetGraph_StorageError_Propagates(t *testing.T) {
	expected := errors.New("boom")
	ctrl := gomock.NewController(t)
	storagemock := storagemock.NewMockStorage(ctrl)
	storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{}, expected)
	c := NewController(context.Background(), Params{
		Logger:  zaptest.NewLogger(t),
		Storage: storagemock,
	})
	_, err := c.GetTargetGraph(t.Context(), entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{Remote: "repo:go-code", BaseSha: "sha"},
	}, testRepositoryConfig)
	require.Error(t, err)
	assert.ErrorIs(t, err, expected)
}

func TestGetTargetGraph_SendsWhenItemPresent(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := storagemock.NewMockStorage(ctrl)
	graphBytes := encodeGraphChunks(t, []entity.GetTargetGraphResponse{{Targets: []entity.OptimizedTarget{}}})

	gomock.InOrder(
		store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{ReadCloser: newMockReadCloser([]byte("treehash-xyz"))}, nil),
		store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{ReadCloser: newMockReadCloser(graphBytes)}, nil),
	)
	c := NewController(context.Background(), Params{
		Logger:  zaptest.NewLogger(t),
		Storage: store,
	})
	reader, err := c.GetTargetGraph(t.Context(), entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{Remote: "repo:go-code", BaseSha: "sha"},
	}, testRepositoryConfig)
	require.NoError(t, err)
	assert.Len(t, drainGraphReader(t, reader), 1)
}

// New coverage: Storage returns NotFound on treehash path -> orchestrator is called to compute the target graph.
func TestGetTargetGraph_TreehashNotFound_NoError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := storagemock.NewMockStorage(ctrl)
	store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{}, storage.NewNotFoundError("x"))
	orchestrator := orchestratormock.NewMockOrchestrator(ctrl)
	graphReader := newGraphReader(t, entity.GetTargetGraphResponse{Targets: []entity.OptimizedTarget{}})
	orchestrator.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any()).Return(graphReader, nil)
	c := NewController(context.Background(), Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      store,
		Orchestrator: orchestrator,
	})
	reader, err := c.GetTargetGraph(t.Context(), entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{Remote: "repo:go-code", BaseSha: "sha"},
	}, testRepositoryConfig)
	require.NoError(t, err)
	assert.Len(t, drainGraphReader(t, reader), 1)
}

// New coverage: io.ReadAll fails on treehash read -> error returned.
func TestGetTargetGraph_TreehashReadError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := storagemock.NewMockStorage(ctrl)
	store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{ReadCloser: &errReadCloser{err: errors.New("readfail")}}, nil)
	c := NewController(context.Background(), Params{
		Logger:  zaptest.NewLogger(t),
		Storage: store,
	})
	_, err := c.GetTargetGraph(t.Context(), entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{Remote: "repo:go-code", BaseSha: "sha"},
	}, testRepositoryConfig)
	assert.Error(t, err)
}

// New coverage: graph fetch returns error -> classified as graph_fetch/infra.
func TestGetTargetGraph_GraphFetchError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := storagemock.NewMockStorage(ctrl)
	gomock.InOrder(
		store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{ReadCloser: newMockReadCloser([]byte("treehash-abc"))}, nil),
		store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{}, errors.New("graph error")),
	)
	c := NewController(context.Background(), Params{
		Logger:  zaptest.NewLogger(t),
		Storage: store,
	})
	_, err := c.GetTargetGraph(t.Context(), entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{Remote: "repo:go-code", BaseSha: "sha"},
	}, testRepositoryConfig)
	require.Error(t, err)
}

func TestGetTargetGraph_GraphNotFound_FallsThrough(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := storagemock.NewMockStorage(ctrl)
	gomock.InOrder(
		store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{ReadCloser: newMockReadCloser([]byte("treehash-abc"))}, nil),
		store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{}, storage.NewNotFoundError("graphs/abc")),
	)
	orch := orchestratormock.NewMockOrchestrator(ctrl)
	graphReader := newGraphReader(t, entity.GetTargetGraphResponse{Targets: []entity.OptimizedTarget{}})
	orch.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any()).Return(graphReader, nil)
	c := NewController(context.Background(), Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      store,
		Orchestrator: orch,
	})
	reader, err := c.GetTargetGraph(t.Context(), entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{Remote: "repo:go-code", BaseSha: "sha"},
	}, testRepositoryConfig)
	require.NoError(t, err)
	assert.Len(t, drainGraphReader(t, reader), 1)
}

func TestGetTargetGraph_GraphReadCancelled(t *testing.T) {
	ctrl := gomock.NewController(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := storagemock.NewMockStorage(ctrl)
	gomock.InOrder(
		store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{ReadCloser: newMockReadCloser([]byte("treehash-abc"))}, nil),
		store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{}, errors.New("context canceled")),
	)
	c := NewController(context.Background(), Params{
		Logger:  zaptest.NewLogger(t),
		Storage: store,
	})
	_, err := c.GetTargetGraph(ctx, entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{Remote: "repo:go-code", BaseSha: "sha"},
	}, testRepositoryConfig)
	require.Error(t, err)
}

func TestGetTargetGraph_OrchestratorCancelled(t *testing.T) {
	ctrl := gomock.NewController(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := storagemock.NewMockStorage(ctrl)
	store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{}, storage.NewNotFoundError("x"))
	orch := orchestratormock.NewMockOrchestrator(ctrl)
	orch.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any()).Return(nil, errors.New("context canceled"))
	c := NewController(context.Background(), Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      store,
		Orchestrator: orch,
	})
	_, err := c.GetTargetGraph(ctx, entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{Remote: "repo:go-code", BaseSha: "sha"},
	}, testRepositoryConfig)
	require.Error(t, err)
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
