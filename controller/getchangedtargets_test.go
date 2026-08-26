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
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/config"
	"github.com/uber/tango/core/storage"
	storagemock "github.com/uber/tango/core/storage/storagemock"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/observability/metrics"
	orchestratormock "github.com/uber/tango/orchestrator/orchestratormock"
	pb "github.com/uber/tango/tangopb"
	tangomock "github.com/uber/tango/tangopb/tangopbmock"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func TestValidateGetChangedTargetsRequest(t *testing.T) {
	tests := []struct {
		name    string
		request *pb.GetChangedTargetsRequest
		wantErr bool
	}{
		{
			name:    "nil request",
			request: nil,
			wantErr: true,
		},
		{
			name: "missing first revision",
			request: &pb.GetChangedTargetsRequest{
				SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing second revision",
			request: &pb.GetChangedTargetsRequest{
				FirstRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
			},
			wantErr: true,
		},
		{
			name: "missing first revision remote",
			request: &pb.GetChangedTargetsRequest{
				FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing first revision base_sha",
			request: &pb.GetChangedTargetsRequest{
				FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code"},
				SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing second revision remote",
			request: &pb.GetChangedTargetsRequest{
				FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing second revision base_sha",
			request: &pb.GetChangedTargetsRequest{
				FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code"},
			},
			wantErr: true,
		},
		{
			name: "different remotes",
			request: &pb.GetChangedTargetsRequest{
				FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:other", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing output_config defaults to no filtering",
			request: &pb.GetChangedTargetsRequest{
				FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
			},
		},
		{
			name: "valid request",
			request: &pb.GetChangedTargetsRequest{
				FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
				OutputConfig:   &pb.OutputConfig{MaxDistance: -1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGetChangedTargetsRequest(tt.request)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCompareTargetGraphs(t *testing.T) {
	c := newTestController(zap.NewNop())

	firstGraph := entity.GetTargetGraphResponse{Metadata: &entity.Metadata{}}
	secondGraph := entity.GetTargetGraphResponse{Metadata: &entity.Metadata{}}

	response, err := c.compareTargetGraphs(t.Context(), c.emitter, zap.NewNop(), []entity.GetTargetGraphResponse{firstGraph}, []entity.GetTargetGraphResponse{secondGraph}, nil)
	require.NoError(t, err)
	require.NotNil(t, response)
}

func TestAllTargetsFileChanged(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		first  map[string]string
		second map[string]string
		want   bool
	}{
		{
			name: "both nil",
			want: false,
		},
		{
			name:   "configured state missing from first",
			second: map[string]string{".bazelrc": "abc"},
			want:   true,
		},
		{
			name:  "configured state missing from second",
			first: map[string]string{".bazelrc": "abc"},
			want:  true,
		},
		{
			name:   "same hashes",
			first:  map[string]string{".bazelrc": "abc", "tools/bazel": "def"},
			second: map[string]string{".bazelrc": "abc", "tools/bazel": "def"},
			want:   false,
		},
		{
			name:   "same missing files",
			first:  map[string]string{".bazelrc": "", "tools/bazel": ""},
			second: map[string]string{".bazelrc": "", "tools/bazel": ""},
			want:   false,
		},
		{
			name:   "different hash",
			first:  map[string]string{".bazelrc": "abc"},
			second: map[string]string{".bazelrc": "changed"},
			want:   true,
		},
		{
			name:   "configured file added",
			first:  map[string]string{".bazelrc": ""},
			second: map[string]string{".bazelrc": "abc"},
			want:   true,
		},
		{
			name:   "configured file deleted",
			first:  map[string]string{".bazelrc": "abc"},
			second: map[string]string{".bazelrc": ""},
			want:   true,
		},
		{
			name:   "configured key missing from first",
			first:  map[string]string{".bazelrc": "abc"},
			second: map[string]string{".bazelrc": "abc", "tools/bazel": "def"},
			want:   true,
		},
		{
			name:   "configured key missing from second",
			first:  map[string]string{".bazelrc": "abc", "tools/bazel": "def"},
			second: map[string]string{".bazelrc": "abc"},
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			first := &entity.Metadata{AllTargetsFileHashes: tt.first}
			second := &entity.Metadata{AllTargetsFileHashes: tt.second}
			assert.Equal(t, tt.want, allTargetsFileChanged(first, second))
		})
	}
}

func TestCompareTargetGraphs_AllTargetsFileTrigger(t *testing.T) {
	c := newTestController(zap.NewNop())

	firstGraph := []entity.GetTargetGraphResponse{
		{Targets: []entity.OptimizedTarget{
			{ID: 1, Hash: "h1", RuleType: 100},
			{ID: 2, Hash: "h2", RuleType: 100},
		}},
		{Metadata: &entity.Metadata{
			TargetIDMapping:      map[int32]string{1: "//app:a", 2: "//app:b"},
			RuleTypeMapping:      map[int32]string{100: "go_library"},
			AllTargetsFileHashes: map[string]string{".bazelrc": "old-hash"},
		}},
	}
	secondGraph := []entity.GetTargetGraphResponse{
		{Targets: []entity.OptimizedTarget{
			{ID: 1, Hash: "h1", RuleType: 100},
			{ID: 2, Hash: "h2", RuleType: 100},
			{ID: 3, Hash: "h3", RuleType: 100},
			{ID: 4, Hash: "h4", RuleType: 100, DirectDependencies: []int32{3}},
		}},
		{Metadata: &entity.Metadata{
			TargetIDMapping:      map[int32]string{1: "//app:a", 2: "//app:b", 3: "//app:c", 4: "//lib:util"},
			RuleTypeMapping:      map[int32]string{100: "go_library"},
			AllTargetsFileHashes: map[string]string{".bazelrc": "new-hash"},
		}},
	}

	responses, err := c.compareTargetGraphs(t.Context(), c.emitter, zap.NewNop(), firstGraph, secondGraph, nil)
	require.NoError(t, err)

	var totalChanged int
	for _, resp := range responses {
		totalChanged += len(resp.ChangedTargets)
	}
	assert.Equal(t, 4, totalChanged, "all targets from second graph should be reported as changed")
	for _, resp := range responses {
		for _, ct := range resp.ChangedTargets {
			assert.Equal(t, entity.ChangeTypeChanged, ct.ChangeType)
			assert.Equal(t, int32(0), ct.Distance)
			assert.NotNil(t, ct.NewTarget)
		}
	}
}

func TestCompareTargetGraphs_AllTargetsFileNoTrigger(t *testing.T) {
	c := newTestController(zap.NewNop())

	firstGraph := []entity.GetTargetGraphResponse{
		{Targets: []entity.OptimizedTarget{
			{ID: 1, Hash: "h1", RuleType: 100},
		}},
		{Metadata: &entity.Metadata{
			TargetIDMapping:      map[int32]string{1: "//app:a"},
			RuleTypeMapping:      map[int32]string{100: "go_library"},
			AllTargetsFileHashes: map[string]string{".bazelrc": "same-hash"},
		}},
	}
	secondGraph := []entity.GetTargetGraphResponse{
		{Targets: []entity.OptimizedTarget{
			{ID: 1, Hash: "h1", RuleType: 100},
		}},
		{Metadata: &entity.Metadata{
			TargetIDMapping:      map[int32]string{1: "//app:a"},
			RuleTypeMapping:      map[int32]string{100: "go_library"},
			AllTargetsFileHashes: map[string]string{".bazelrc": "same-hash"},
		}},
	}

	responses, err := c.compareTargetGraphs(t.Context(), c.emitter, zap.NewNop(), firstGraph, secondGraph, nil)
	require.NoError(t, err)

	var totalChanged int
	for _, resp := range responses {
		totalChanged += len(resp.ChangedTargets)
	}
	assert.Equal(t, 0, totalChanged, "no targets should be changed when AllTargetsFiles hashes match")
}

func TestGetChangedTargets_ValidationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)

	c := NewController(context.Background(), Params{Logger: zap.NewNop(), Orchestrator: orchestratormock.NewMockOrchestrator(ctrl)})

	err := c.GetChangedTargets(nil, stream)
	require.Error(t, err)
}

func TestGetChangedTargets_CacheHit(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())

	// Build a cached response with one ChangedTargets message and one Metadata message,
	// gob-encoded (the storage layer streams a gob-encoded sequence of values).
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	enc.Encode(entity.GetChangedTargetsResponse{ChangedTargets: []entity.ChangedTarget{}})
	enc.Encode(entity.GetChangedTargetsResponse{Metadata: &entity.Metadata{}})
	cachedBytes := buf.Bytes()

	storagemock := storagemock.NewMockStorage(ctrl)
	// First two Gets resolve the treehashes, third gets the cached comparison result.
	gomock.InOrder(
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash1")))}, nil),
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash2")))}, nil),
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(cachedBytes))}, nil),
	)

	stream.EXPECT().Send(gomock.Any()).Return(nil).Times(2)

	c := NewController(context.Background(), Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      storagemock,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	request := &pb.GetChangedTargetsRequest{
		FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
		OutputConfig:   &pb.OutputConfig{MaxDistance: -1},
	}

	err := c.GetChangedTargets(request, stream)
	require.NoError(t, err)
}

func TestGetChangedTargets_TreehashReadError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())

	storagemock := storagemock.NewMockStorage(ctrl)
	// A non-NotFound storage error on a treehash read must surface as a failed
	// request rather than be silently treated as a cache miss. Both revision
	// treehashes are read in parallel, so two Get calls happen; the handler
	// returns the first failure (and drops the cancelled sibling's error)
	// before any graph fetch happens.
	injected := errors.New("storage exploded")
	storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).
		Return(storage.DownloadResponse{}, injected).Times(2)

	c := NewController(context.Background(), Params{
		Logger:       zap.NewNop(),
		Storage:      storagemock,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	request := &pb.GetChangedTargetsRequest{
		FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
		OutputConfig:   &pb.OutputConfig{MaxDistance: -1},
	}

	err := c.GetChangedTargets(request, stream)
	require.Error(t, err)
	assert.Contains(t, err.Error(), injected.Error())
}

func TestReadTreehash(t *testing.T) {
	bd := &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"}

	t.Run("cache miss returns empty and no error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		st := storagemock.NewMockStorage(ctrl)
		st.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(storage.DownloadResponse{}, storage.NewNotFoundError("missing"))

		val, err := readTreehash(t.Context(), st, bd, metrics.Nop(), opGetChangedTargets)
		require.NoError(t, err)
		assert.Empty(t, val)
	})

	t.Run("storage error surfaces", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		st := storagemock.NewMockStorage(ctrl)
		injected := errors.New("infra down")
		st.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(storage.DownloadResponse{}, injected)

		val, err := readTreehash(t.Context(), st, bd, metrics.Nop(), opGetChangedTargets)
		require.Error(t, err)
		assert.ErrorIs(t, err, injected)
		assert.Empty(t, val)
	})

	t.Run("success returns treehash", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		st := storagemock.NewMockStorage(ctrl)
		st.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(storage.DownloadResponse{ReadCloser: io.NopCloser(strings.NewReader("deadbeef"))}, nil)

		val, err := readTreehash(t.Context(), st, bd, metrics.Nop(), opGetChangedTargets)
		require.NoError(t, err)
		assert.Equal(t, "deadbeef", val)
	})
}

func TestGetChangedTargets_StreamSendError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())

	stream.EXPECT().Send(gomock.Any()).Return(errors.New("send error"))
	storagemock := storagemock.NewMockStorage(ctrl)

	var buf bytes.Buffer
	gob.NewEncoder(&buf).Encode(entity.GetTargetGraphResponse{})
	storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, req storage.DownloadRequest) (storage.DownloadResponse, error) {
		if strings.Contains(req.Key, "compared-targets") {
			return storage.DownloadResponse{}, storage.NewNotFoundError(req.Key)
		}
		if strings.Contains(req.Key, "treehashes") {
			return storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("th")))}, nil
		}
		if strings.Contains(req.Key, "graphs") {
			return storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(buf.Bytes()))}, nil
		}
		return storage.DownloadResponse{}, fmt.Errorf("unexpected key: %s", req.Key)
	}).AnyTimes()

	// Put is launched in a goroutine — use a channel to wait for it before the test ends.
	putDone := make(chan struct{}, 1)
	storagemock.EXPECT().Put(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ storage.UploadRequest) error {
		putDone <- struct{}{}
		return nil
	})

	c := NewController(context.Background(), Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      storagemock,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	request := &pb.GetChangedTargetsRequest{
		FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
		OutputConfig:   &pb.OutputConfig{MaxDistance: -1},
	}

	err := c.GetChangedTargets(request, stream)
	assert.Error(t, err)

	select {
	case <-putDone:
	case <-time.After(time.Second):
		assert.Fail(t, "cache write goroutine did not complete in time")
	}
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

	storagemock := storagemock.NewMockStorage(ctrl)

	// Build first revision graph (2 chunks: Targets + Metadata)
	var buf1 bytes.Buffer
	enc1 := gob.NewEncoder(&buf1)
	enc1.Encode(entity.GetTargetGraphResponse{
		Targets: []entity.OptimizedTarget{
			{ID: 1, Hash: "h1", RuleType: 100},
			{ID: 2, Hash: "h2-old", RuleType: 300},
		},
	})
	enc1.Encode(entity.GetTargetGraphResponse{
		Metadata: &entity.Metadata{
			TargetIDMapping: map[int32]string{1: "//app:target1", 2: "//app:target2"},
			RuleTypeMapping: map[int32]string{100: "go_library", 300: "source file"},
		},
	})
	graph1Bytes := buf1.Bytes()

	// Build second revision graph - target2 has different hash
	var buf2 bytes.Buffer
	enc2 := gob.NewEncoder(&buf2)
	enc2.Encode(entity.GetTargetGraphResponse{
		Targets: []entity.OptimizedTarget{
			{ID: 1, Hash: "h1", RuleType: 100},
			{ID: 2, Hash: "h2-new", RuleType: 300}, // changed hash
		},
	})
	enc2.Encode(entity.GetTargetGraphResponse{
		Metadata: &entity.Metadata{
			TargetIDMapping: map[int32]string{1: "//app:target1", 2: "//app:target2"},
			RuleTypeMapping: map[int32]string{100: "go_library", 300: "source file"},
		},
	})
	graph2Bytes := buf2.Bytes()

	// Each revision needs: treehash lookup + graph lookup. Plus one initial cache miss.
	storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req storage.DownloadRequest) (storage.DownloadResponse, error) {
			switch {
			case strings.Contains(req.Key, "compared-targets"):
				return storage.DownloadResponse{}, storage.NewNotFoundError(req.Key)
			case strings.Contains(req.Key, "sha1"):
				return storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash1")))}, nil
			case strings.Contains(req.Key, "sha2"):
				return storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash2")))}, nil
			case strings.Contains(req.Key, "treehash1"):
				return storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(graph1Bytes))}, nil
			case strings.Contains(req.Key, "treehash2"):
				return storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(graph2Bytes))}, nil
			default:
				return storage.DownloadResponse{}, fmt.Errorf("unexpected key: %s", req.Key)
			}
			// readTreehash (×2 pre) + comparison cache miss (×1) + graph computation (×4) + readTreehash (×2 post) = 9
		}).Times(9)
	// Put is launched in a goroutine — use a channel to wait for it before the test ends.
	putDone := make(chan struct{}, 1)
	storagemock.EXPECT().Put(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ storage.UploadRequest) error {
		putDone <- struct{}{}
		return nil
	})

	c := NewController(context.Background(), Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      storagemock,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	request := &pb.GetChangedTargetsRequest{
		FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
		OutputConfig:   &pb.OutputConfig{MaxDistance: -1, IncludeHashes: true, IncludeTags: true, IncludeAttributes: true},
	}

	err := c.GetChangedTargets(request, stream)
	require.NoError(t, err)

	select {
	case <-putDone:
	case <-time.After(time.Second):
		assert.Fail(t, "cache write goroutine did not complete in time")
	}

	require.Len(t, sentResponses, 2)
	changedTargets := sentResponses[0].GetChangedTargets()
	metadata := sentResponses[1].GetMetadata()

	// Verify target2 is detected as changed (hash changed from h2-old to h2-new)
	require.Len(t, changedTargets.GetChangedTargets(), 1, "should detect 1 changed target")
	changed := changedTargets.GetChangedTargets()[0]
	assert.Equal(t, "h2-old", changed.GetOldTarget().GetHash())
	assert.Equal(t, "h2-new", changed.GetNewTarget().GetHash())

	targetID := changed.GetNewTarget().GetId()
	assert.Equal(t, "//app:target2", metadata.GetTargetIdMapping()[targetID])
}

// TestGetChangedTargets_CacheWriteUsesAppCtx verifies the cache-write
// goroutine passes c.appCtx to storage (so a client disconnect does not abort
// the write, but server shutdown does). Drives a successful GetChangedTargets
// pipeline so the goroutine runs, captures the context the storage backend
// sees inside Put, and asserts each cancellation source independently.
func TestGetChangedTargets_CacheWriteUsesAppCtx(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	reqCtx, cancelReq := context.WithCancel(t.Context())
	defer cancelReq()
	stream.EXPECT().Context().Return(reqCtx)
	stream.EXPECT().Send(gomock.Any()).Return(nil).AnyTimes()

	storagemock := storagemock.NewMockStorage(ctrl)

	// Minimal single-chunk graph so the comparison succeeds and the cache
	// goroutine runs. Both revisions share the same target so there are no
	// diffs to send beyond the metadata chunk.
	var graphBuf bytes.Buffer
	enc := gob.NewEncoder(&graphBuf)
	enc.Encode(entity.GetTargetGraphResponse{
		Targets: []entity.OptimizedTarget{{ID: 1, Hash: "h1", RuleType: 100}},
	})
	enc.Encode(entity.GetTargetGraphResponse{
		Metadata: &entity.Metadata{
			TargetIDMapping: map[int32]string{1: "//app:t1"},
			RuleTypeMapping: map[int32]string{100: "go_library"},
		},
	})
	graphBytes := graphBuf.Bytes()

	storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req storage.DownloadRequest) (storage.DownloadResponse, error) {
			switch {
			case strings.Contains(req.Key, "compared-targets"):
				return storage.DownloadResponse{}, storage.NewNotFoundError(req.Key)
			case strings.Contains(req.Key, "sha1"):
				return storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash1")))}, nil
			case strings.Contains(req.Key, "sha2"):
				return storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash2")))}, nil
			case strings.Contains(req.Key, "treehash"):
				return storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(graphBytes))}, nil
			default:
				return storage.DownloadResponse{}, fmt.Errorf("unexpected key: %s", req.Key)
			}
		}).AnyTimes()

	// Put captures the context the cache-write goroutine passes to storage
	// and blocks until that context is cancelled, mimicking a slow backend.
	cacheCtxCh := make(chan context.Context, 1)
	storagemock.EXPECT().Put(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ storage.UploadRequest) error {
			cacheCtxCh <- ctx
			<-ctx.Done()
			return ctx.Err()
		})

	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()
	c := NewController(appCtx, Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      storagemock,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	request := &pb.GetChangedTargetsRequest{
		FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
		OutputConfig:   &pb.OutputConfig{MaxDistance: -1},
	}

	handlerDone := make(chan error, 1)
	go func() { handlerDone <- c.GetChangedTargets(request, stream) }()

	var cacheCtx context.Context
	select {
	case cacheCtx = <-cacheCtxCh:
	case <-time.After(2 * time.Second):
		t.Fatal("cache-write goroutine never reached storage.Put")
	}

	// Handler should be free to return regardless of the still-running
	// goroutine — that is the whole point of the detached cache write.
	select {
	case err := <-handlerDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("GetChangedTargets did not return while cache write was in flight")
	}

	// Cancelling the request ctx (client disconnect) must NOT cancel the
	// cache write. Give the cancellation a beat to propagate if it were
	// going to.
	cancelReq()
	select {
	case <-cacheCtx.Done():
		t.Fatal("cache-write ctx was cancelled by request ctx; should only follow appCtx")
	case <-time.After(50 * time.Millisecond):
	}

	// Cancelling appCtx (server shutdown) MUST cancel the cache write so
	// the goroutine doesn't outlive the process.
	cancelApp()
	select {
	case <-cacheCtx.Done():
		assert.ErrorIs(t, cacheCtx.Err(), context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("cache-write ctx was not cancelled by appCtx")
	}
}

func TestCompareTargetGraphs_NewTarget_CanonicalIDs(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	first := []entity.GetTargetGraphResponse{
		{
			Metadata: &entity.Metadata{
				TargetIDMapping:             map[int32]string{},
				RuleTypeMapping:             map[int32]string{},
				TagMapping:                  map[int32]string{},
				AttributeNameMapping:        map[int32]string{},
				AttributeStringValueMapping: map[int32]string{},
			},
		},
	}
	second := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{ID: 10, Hash: "h2", RuleType: 1},
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping:             map[int32]string{10: "//app:new"},
				RuleTypeMapping:             map[int32]string{1: "rule"},
				TagMapping:                  map[int32]string{},
				AttributeNameMapping:        map[int32]string{},
				AttributeStringValueMapping: map[int32]string{},
			},
		},
	}
	res, err := c.compareTargetGraphs(t.Context(), c.emitter, zap.NewNop(), first, second, nil)
	require.NoError(t, err)
	require.Len(t, res, 2)
	cs := res[0].ChangedTargets
	require.NotNil(t, cs)
	require.Len(t, cs, 1)
	ct := cs[0]
	require.Equal(t, entity.ChangeTypeNew, ct.ChangeType)
	// ID used in target should match canonical metadata mapping
	meta := res[1].Metadata
	require.NotNil(t, meta)
	newID := ct.NewTarget.ID
	require.Equal(t, "//app:new", meta.TargetIDMapping[newID])
}

func TestCompareTargetGraphs_SourceFileDirectAndPropagation(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Old: source file A (id 1, hash h1), lib L (id 2, hash h1, dep -> A)
	first := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{ID: 1, Hash: "h1", RuleType: 100},                                 // "source file"
				{ID: 2, Hash: "h1", RuleType: 200, DirectDependencies: []int32{1}}, // "rule"
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{
					1: "//app:A",
					2: "//app:L",
				},
				RuleTypeMapping: map[int32]string{
					100: "source file",
					200: "rule",
				},
			},
		},
	}
	// New: both change hashes; same structure
	second := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{ID: 11, Hash: "h2", RuleType: 101},                                  // "source file"
				{ID: 22, Hash: "h2", RuleType: 201, DirectDependencies: []int32{11}}, // "rule"
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{
					11: "//app:A",
					22: "//app:L",
				},
				RuleTypeMapping: map[int32]string{
					101: "source file",
					201: "rule",
				},
			},
		},
	}
	res, err := c.compareTargetGraphs(t.Context(), c.emitter, zap.NewNop(), first, second, nil)
	require.NoError(t, err)
	cs := res[0].ChangedTargets
	require.NotNil(t, cs)
	// Expect 2 changed: A (source-file seed, distance 0) and L (rule whose own src changed, distance 0)
	require.Len(t, cs, 2)
	var aCT, lCT *entity.ChangedTarget
	for i := range cs {
		if cs[i].NewTarget == nil {
			continue
		}
		name := res[1].Metadata.TargetIDMapping[cs[i].NewTarget.ID]
		if name == "//app:A" {
			aCT = &cs[i]
		}
		if name == "//app:L" {
			lCT = &cs[i]
		}
	}
	require.NotNil(t, aCT)
	require.NotNil(t, lCT)
	require.Equal(t, entity.ChangeTypeChanged, aCT.ChangeType)
	require.Equal(t, entity.ChangeTypeChanged, lCT.ChangeType)
	assert.Equal(t, int32(0), aCT.Distance, "source-file A with hash change is a seed (distance 0)")
	assert.Equal(t, int32(0), lCT.Distance, "rule L whose own source A changed is a seed (distance 0)")
	// Old and new IDs must match for each changed target under canonical metadata
	require.Equal(t, aCT.OldTarget.ID, aCT.NewTarget.ID)
	require.Equal(t, lCT.OldTarget.ID, lCT.NewTarget.ID)
}

func TestCompareTargetGraphs_ChangedRuleUnreachableFromAnySeed(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Old: T (id 1, rule), no deps
	first := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{ID: 1, Hash: "h1", RuleType: 200},
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{1: "//app:T"},
				RuleTypeMapping: map[int32]string{100: "source file", 200: "rule"},
			},
		},
	}
	// New: T hash changed, still no deps
	second := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{ID: 2, Hash: "h2", RuleType: 201},
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{2: "//app:T"},
				RuleTypeMapping: map[int32]string{101: "source file", 201: "rule"},
			},
		},
	}
	// Hash-only change on a rule with no own-config change and no reachable
	// seed: under "trust the hasher" semantics, an orphan CHANGED rule with
	// no upstream explanation becomes a distance-0 seed itself.
	res, err := c.compareTargetGraphs(t.Context(), c.emitter, zap.NewNop(), first, second, nil)
	require.NoError(t, err)
	cs := res[0].ChangedTargets
	require.NotNil(t, cs)
	require.Len(t, cs, 1)
	got := cs[0]
	require.Equal(t, entity.ChangeTypeChanged, got.ChangeType)
	assert.Equal(t, int32(0), got.Distance, "orphan hash change is seeded by trust-the-hasher")
}

func TestCompareTargetGraphs_ChangedWhenDependenciesChanged(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Old: T (id 1, rule) with deps on A
	first := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{ID: 1, Hash: "h1", RuleType: 200, DirectDependencies: []int32{10}},
				{ID: 10, Hash: "h1", RuleType: 200}, // Dependency A
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{
					1:  "//app:T",
					10: "//app:A",
				},
				RuleTypeMapping: map[int32]string{
					100: "source file",
					200: "rule",
				},
			},
		},
	}
	// New: T now depends on B instead of A (hash changed due to dep change)
	second := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{ID: 2, Hash: "h2", RuleType: 201, DirectDependencies: []int32{20}},
				{ID: 20, Hash: "h1", RuleType: 201}, // Dependency B
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{
					2:  "//app:T",
					20: "//app:B",
				},
				RuleTypeMapping: map[int32]string{
					101: "source file",
					201: "rule",
				},
			},
		},
	}
	res, err := c.compareTargetGraphs(t.Context(), c.emitter, zap.NewNop(), first, second, nil)
	require.NoError(t, err)
	cs := res[0].ChangedTargets
	require.NotNil(t, cs)

	// Find target T in the changed targets
	var targetT *entity.ChangedTarget
	for i := range cs {
		if cs[i].NewTarget == nil {
			continue
		}
		name := res[1].Metadata.TargetIDMapping[cs[i].NewTarget.ID]
		if name == "//app:T" {
			targetT = &cs[i]
			break
		}
	}
	require.NotNil(t, targetT)
	require.Equal(t, entity.ChangeTypeChanged, targetT.ChangeType, "Target with changed dependencies should be marked as CHANGED")
	assert.Equal(t, int32(0), targetT.Distance, "Target whose dep-name set changed is a seed (distance 0)")
}

func TestCompareTargetGraphs_ChangedWhenAttributesChanged(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Old: T with attribute "key1" -> "value1"
	first := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{
					ID:         1,
					Hash:       "h1",
					RuleType:   200,
					Attributes: map[int32]int32{1: 10}, // attr name 1 -> attr value 10
				},
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{1: "//app:T"},
				RuleTypeMapping: map[int32]string{
					100: "source file",
					200: "rule",
				},
				AttributeNameMapping:        map[int32]string{1: "key1"},
				AttributeStringValueMapping: map[int32]string{10: "value1"},
			},
		},
	}
	// New: T with attribute "key1" -> "value2" (changed value)
	second := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{
					ID:         2,
					Hash:       "h2",
					RuleType:   201,
					Attributes: map[int32]int32{2: 20}, // attr name 2 -> attr value 20
				},
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{2: "//app:T"},
				RuleTypeMapping: map[int32]string{
					101: "source file",
					201: "rule",
				},
				AttributeNameMapping:        map[int32]string{2: "key1"},
				AttributeStringValueMapping: map[int32]string{20: "value2"},
			},
		},
	}
	res, err := c.compareTargetGraphs(t.Context(), c.emitter, zap.NewNop(), first, second, nil)
	require.NoError(t, err)
	cs := res[0].ChangedTargets
	require.NotNil(t, cs)
	require.Len(t, cs, 1)
	got := cs[0]
	require.Equal(t, entity.ChangeTypeChanged, got.ChangeType, "Target with changed attributes should be marked as CHANGED")
	assert.Equal(t, int32(0), got.Distance, "Target with own-config (attrs) change is a seed (distance 0)")
}

func TestCompareTargetGraphs_ChangedWhenNewAttributeAdded(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Old: T with one attribute
	first := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{
					ID:         1,
					Hash:       "h1",
					RuleType:   200,
					Attributes: map[int32]int32{1: 10},
				},
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{1: "//app:T"},
				RuleTypeMapping: map[int32]string{
					100: "source file",
					200: "rule",
				},
				AttributeNameMapping:        map[int32]string{1: "key1"},
				AttributeStringValueMapping: map[int32]string{10: "value1"},
			},
		},
	}
	// New: T with two attributes (added key2)
	second := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{
					ID:       2,
					Hash:     "h2",
					RuleType: 201,
					Attributes: map[int32]int32{
						2: 20, // key1 -> value1
						3: 30, // key2 -> value2 (NEW)
					},
				},
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{2: "//app:T"},
				RuleTypeMapping: map[int32]string{
					101: "source file",
					201: "rule",
				},
				AttributeNameMapping: map[int32]string{
					2: "key1",
					3: "key2",
				},
				AttributeStringValueMapping: map[int32]string{
					20: "value1",
					30: "value2",
				},
			},
		},
	}
	res, err := c.compareTargetGraphs(t.Context(), c.emitter, zap.NewNop(), first, second, nil)
	require.NoError(t, err)
	cs := res[0].ChangedTargets
	require.NotNil(t, cs)
	require.Len(t, cs, 1)
	got := cs[0]
	require.Equal(t, entity.ChangeTypeChanged, got.ChangeType, "Target with new attribute added should be marked as CHANGED")
	assert.Equal(t, int32(0), got.Distance, "Target with own-config (attrs) change is a seed (distance 0)")
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

	stream.EXPECT().Send(gomock.Any()).Return(errors.New("send error"))

	err := sendTrimmedChangedTargets(stream, responses, -1, nil)
	assert.EqualError(t, err, "send error")
}

func TestGetChangedTargets_CacheHitWithDistanceFilter(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())

	// Cached response: two targets at distances 0 and 2, plus metadata.
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	enc.Encode(entity.GetChangedTargetsResponse{
		ChangedTargets: []entity.ChangedTarget{
			{Distance: 0, ChangeType: entity.ChangeTypeChanged},
			{Distance: 2, ChangeType: entity.ChangeTypeChanged},
		},
	})
	enc.Encode(entity.GetChangedTargetsResponse{Metadata: &entity.Metadata{}})
	cachedBytes := buf.Bytes()

	storagemock := storagemock.NewMockStorage(ctrl)
	gomock.InOrder(
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash1")))}, nil),
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash2")))}, nil),
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(cachedBytes))}, nil),
	)

	var sent []*pb.GetChangedTargetsResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(r *pb.GetChangedTargetsResponse, _ ...any) error {
		sent = append(sent, r)
		return nil
	}).Times(2)

	c := NewController(context.Background(), Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      storagemock,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	request := &pb.GetChangedTargetsRequest{
		FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
		OutputConfig:   &pb.OutputConfig{MaxDistance: 1},
	}

	err := c.GetChangedTargets(request, stream)
	require.NoError(t, err)

	require.Len(t, sent, 2)
	kept := sent[0].GetChangedTargets().GetChangedTargets()
	require.Len(t, kept, 1, "only the distance-0 target should survive the filter")
	assert.Equal(t, int32(0), kept[0].GetDistance())
	// Metadata always forwarded
	assert.NotNil(t, sent[1].GetMetadata())
}

func TestCompareTargetGraphs_HashOnlyChangePropagatesViaBFS(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Old: T (rule) with deps on source file A (id 10) and attributes
	first := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{
					ID:                 1,
					Hash:               "h1",
					RuleType:           200,
					DirectDependencies: []int32{10},
					Attributes:         map[int32]int32{1: 10},
				},
				{ID: 10, Hash: "h1", RuleType: 100}, // source file A
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{
					1:  "//app:T",
					10: "//app:A",
				},
				RuleTypeMapping: map[int32]string{
					100: "source file",
					200: "rule",
				},
				AttributeNameMapping:        map[int32]string{1: "key1"},
				AttributeStringValueMapping: map[int32]string{10: "value1"},
			},
		},
	}
	// New: source file A's hash changed (a seed); T's own config (deps, attrs)
	// is unchanged but its hash differs because of A.
	second := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{
					ID:                 2,
					Hash:               "h2", // Changed
					RuleType:           201,
					DirectDependencies: []int32{20},            // Same dep name (//app:A)
					Attributes:         map[int32]int32{2: 20}, // Same attribute
				},
				{ID: 20, Hash: "h2", RuleType: 101}, // source file A, hash changed
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{
					2:  "//app:T",
					20: "//app:A",
				},
				RuleTypeMapping: map[int32]string{
					101: "source file",
					201: "rule",
				},
				AttributeNameMapping:        map[int32]string{2: "key1"},
				AttributeStringValueMapping: map[int32]string{20: "value1"},
			},
		},
	}
	res, err := c.compareTargetGraphs(t.Context(), c.emitter, zap.NewNop(), first, second, nil)
	require.NoError(t, err)
	cs := res[0].ChangedTargets
	require.NotNil(t, cs)

	// Find target T
	var targetT *entity.ChangedTarget
	for i := range cs {
		if cs[i].NewTarget == nil {
			continue
		}
		name := res[1].Metadata.TargetIDMapping[cs[i].NewTarget.ID]
		if name == "//app:T" {
			targetT = &cs[i]
			break
		}
	}
	require.NotNil(t, targetT)
	require.Equal(t, entity.ChangeTypeChanged, targetT.ChangeType, "Target with only hash change (not deps/attrs) is CHANGED")
	assert.Equal(t, int32(0), targetT.Distance, "T owns changed source file A so is a seed (distance 0)")
}

func TestCompareTargetGraphs_SiblingRuleNotPromotedToSeed(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Source file A (id 1) owned by rule L (id 2).
	// Rule T (id 3) depends on L (sibling rule), NOT directly on A.
	// When A changes, L should be distance 0 (owns its changed src),
	// but T should be distance 1 (depends on changed rule, not its own src).
	first := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{ID: 1, Hash: "h1", RuleType: 100},                                 // source file A
				{ID: 2, Hash: "h1", RuleType: 200, DirectDependencies: []int32{1}}, // rule L -> A
				{ID: 3, Hash: "h1", RuleType: 200, DirectDependencies: []int32{2}}, // rule T -> L
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{
					1: "//pkg:A",
					2: "//pkg:L",
					3: "//pkg:T",
				},
				RuleTypeMapping: map[int32]string{
					100: "source file",
					200: "rule",
				},
			},
		},
	}
	second := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{ID: 11, Hash: "h2", RuleType: 101},                                  // source file A changed
				{ID: 22, Hash: "h2", RuleType: 201, DirectDependencies: []int32{11}}, // rule L -> A
				{ID: 33, Hash: "h2", RuleType: 201, DirectDependencies: []int32{22}}, // rule T -> L
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{
					11: "//pkg:A",
					22: "//pkg:L",
					33: "//pkg:T",
				},
				RuleTypeMapping: map[int32]string{
					101: "source file",
					201: "rule",
				},
			},
		},
	}
	res, err := c.compareTargetGraphs(t.Context(), c.emitter, zap.NewNop(), first, second, nil)
	require.NoError(t, err)
	cs := res[0].ChangedTargets
	require.NotNil(t, cs)
	require.Len(t, cs, 3)

	byName := make(map[string]*entity.ChangedTarget)
	for i := range cs {
		if cs[i].NewTarget == nil {
			continue
		}
		name := res[1].Metadata.TargetIDMapping[cs[i].NewTarget.ID]
		byName[name] = &cs[i]
	}
	assert.Equal(t, int32(0), byName["//pkg:A"].Distance, "source file A is a seed")
	assert.Equal(t, int32(0), byName["//pkg:L"].Distance, "rule L owns changed source A → seed")
	assert.Equal(t, int32(1), byName["//pkg:T"].Distance, "rule T depends on sibling rule L, not its own src → distance 1")
}

func TestCompareTargetGraphs_DeletedTargetEmitted(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Old: T (rule) exists; New: T is gone.
	first := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{
				{ID: 1, Hash: "h1", RuleType: 200},
			},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{1: "//app:T"},
				RuleTypeMapping: map[int32]string{100: "source file", 200: "rule"},
			},
		},
	}
	second := []entity.GetTargetGraphResponse{
		{
			Targets: []entity.OptimizedTarget{},
		},
		{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{},
				RuleTypeMapping: map[int32]string{101: "source file", 201: "rule"},
			},
		},
	}
	res, err := c.compareTargetGraphs(t.Context(), c.emitter, zap.NewNop(), first, second, nil)
	require.NoError(t, err)
	cs := res[0].ChangedTargets
	require.NotNil(t, cs)
	require.Len(t, cs, 1)
	got := cs[0]
	require.Equal(t, entity.ChangeTypeDeleted, got.ChangeType)
	require.NotNil(t, got.OldTarget, "DELETED entry must carry OldTarget")
	assert.Nil(t, got.NewTarget, "DELETED entry must not carry NewTarget")
	assert.Equal(t, int32(0), got.Distance, "DELETED targets are seeds (distance 0)")
	// Old id is remapped into the canonical id space; metadata must resolve back to the deleted name.
	assert.Equal(t, "//app:T", res[1].Metadata.TargetIDMapping[got.OldTarget.ID])
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

func changedTargetsRequest() *pb.GetChangedTargetsRequest {
	return &pb.GetChangedTargetsRequest{
		FirstRevision:  &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Strategy: pb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
		OutputConfig:   &pb.OutputConfig{MaxDistance: -1},
	}
}

func TestServeChangedTargetsFromCache(t *testing.T) {
	t.Run("cache miss returns not-served, no error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		st := storagemock.NewMockStorage(ctrl)
		// Both treehash reads miss, so the cache path is skipped entirely.
		st.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(storage.DownloadResponse{}, storage.NewNotFoundError("missing")).Times(2)

		c := newTestController(zaptest.NewLogger(t))
		c.storage = st
		stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)

		served, err := c.serveChangedTargetsFromCache(t.Context(), c.emitter, c.logger, changedTargetsRequest(), stream, -1, time.Now())
		require.NoError(t, err)
		assert.False(t, served, "a cache miss must not be served")
	})

	t.Run("corrupt cached blob falls through to recompute", func(t *testing.T) {
		ctrl := gomock.NewController(t)

		// A two-message gob blob with the second message truncated — mimics an
		// incomplete concurrent write. The reader returns the first message fine
		// but errors on the second, and the caller must fall through
		// (served=false) without sending anything.
		var buf bytes.Buffer
		enc := gob.NewEncoder(&buf)
		enc.Encode(entity.GetChangedTargetsResponse{ChangedTargets: []entity.ChangedTarget{}})
		enc.Encode(entity.GetChangedTargetsResponse{Metadata: &entity.Metadata{}})
		// Truncate well into the second gob message to guarantee corruption.
		truncated := buf.Bytes()[:buf.Len()-5]

		st := storagemock.NewMockStorage(ctrl)
		st.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, req storage.DownloadRequest) (storage.DownloadResponse, error) {
				switch {
				case strings.Contains(req.Key, "compared-targets"):
					return storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(truncated))}, nil
				case strings.Contains(req.Key, "sha1"):
					return storage.DownloadResponse{ReadCloser: io.NopCloser(strings.NewReader("treehash1"))}, nil
				case strings.Contains(req.Key, "sha2"):
					return storage.DownloadResponse{ReadCloser: io.NopCloser(strings.NewReader("treehash2"))}, nil
				default:
					return storage.DownloadResponse{}, fmt.Errorf("unexpected key: %s", req.Key)
				}
			}).AnyTimes()

		c := newTestController(zaptest.NewLogger(t))
		c.storage = st
		stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
		// No Send expectation: a corrupt blob must not send anything to the client.

		served, err := c.serveChangedTargetsFromCache(t.Context(), c.emitter, c.logger, changedTargetsRequest(), stream, -1, time.Now())
		require.NoError(t, err)
		assert.False(t, served, "a corrupt blob must trigger recompute, not a partial send")
	})

	t.Run("clean hit is served", func(t *testing.T) {
		ctrl := gomock.NewController(t)

		var buf bytes.Buffer
		enc := gob.NewEncoder(&buf)
		enc.Encode(entity.GetChangedTargetsResponse{ChangedTargets: []entity.ChangedTarget{}})
		enc.Encode(entity.GetChangedTargetsResponse{Metadata: &entity.Metadata{}})
		cached := buf.Bytes()

		st := storagemock.NewMockStorage(ctrl)
		st.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, req storage.DownloadRequest) (storage.DownloadResponse, error) {
				switch {
				case strings.Contains(req.Key, "compared-targets"):
					return storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(cached))}, nil
				case strings.Contains(req.Key, "sha1"):
					return storage.DownloadResponse{ReadCloser: io.NopCloser(strings.NewReader("treehash1"))}, nil
				case strings.Contains(req.Key, "sha2"):
					return storage.DownloadResponse{ReadCloser: io.NopCloser(strings.NewReader("treehash2"))}, nil
				default:
					return storage.DownloadResponse{}, fmt.Errorf("unexpected key: %s", req.Key)
				}
			}).AnyTimes()

		c := newTestController(zaptest.NewLogger(t))
		c.storage = st
		stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
		stream.EXPECT().Send(gomock.Any()).Return(nil).Times(2)

		served, err := c.serveChangedTargetsFromCache(t.Context(), c.emitter, c.logger, changedTargetsRequest(), stream, -1, time.Now())
		require.NoError(t, err)
		assert.True(t, served, "a clean cache hit must be served")
	})
}

func TestFetchTargetGraphs(t *testing.T) {
	// BypassCache=true keeps getGraph on the orchestrator path only, so these
	// tests need no storage mock.
	bypassRequest := func() *pb.GetChangedTargetsRequest {
		r := changedTargetsRequest()
		r.BypassCache = true
		return r
	}
	entityChunk := entity.GetTargetGraphResponse{Metadata: &entity.Metadata{}}

	t.Run("returns both graphs on success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		orch := orchestratormock.NewMockOrchestrator(ctrl)
		orch.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, _ entity.GetTargetGraphRequest) (storage.GraphReader, error) {
				return newGraphReader(t, entityChunk), nil
			}).Times(2)

		c := newTestController(zaptest.NewLogger(t))
		c.orchestrator = orch

		first, second, err := c.fetchTargetGraphs(t.Context(), c.emitter, c.logger, bypassRequest())
		require.NoError(t, err)
		require.Len(t, first.chunks, 1)
		require.Len(t, second.chunks, 1)
	})

	t.Run("first revision failure names graph #1", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		injected := errors.New("orchestrator boom")
		causeCh := make(chan error, 1)
		orch := orchestratormock.NewMockOrchestrator(ctrl)
		orch.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, p entity.GetTargetGraphRequest) (storage.GraphReader, error) {
				if p.Build.BaseSha == "sha1" {
					return nil, injected
				}
				<-ctx.Done()
				causeCh <- context.Cause(ctx)
				return nil, ctx.Err()
			}).Times(2)

		c := newTestController(zaptest.NewLogger(t))
		c.orchestrator = orch

		first, second, err := c.fetchTargetGraphs(t.Context(), c.emitter, c.logger, bypassRequest())
		require.Error(t, err)
		assert.ErrorIs(t, err, injected)
		assert.Zero(t, first)
		assert.Zero(t, second)

		cause := <-causeCh
		assert.NotErrorIs(t, cause, context.Canceled, "sibling cancellation should carry a distinct cause, not the generic context.Canceled")
	})

	t.Run("empty reader yields no-chunks error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		orch := orchestratormock.NewMockOrchestrator(ctrl)
		orch.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, _ entity.GetTargetGraphRequest) (storage.GraphReader, error) {
				return newGraphReader(t), nil
			}).Times(2)

		c := newTestController(zaptest.NewLogger(t))
		c.orchestrator = orch

		_, _, err := c.fetchTargetGraphs(t.Context(), c.emitter, c.logger, bypassRequest())
		require.Error(t, err)
	})

	t.Run("panic in fetch is recovered as an error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		orch := orchestratormock.NewMockOrchestrator(ctrl)
		orch.EXPECT().GetTargetGraph(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, _ entity.GetTargetGraphRequest) (storage.GraphReader, error) {
				panic("boom in orchestrator")
			}).Times(2)

		c := newTestController(zaptest.NewLogger(t))
		c.orchestrator = orch

		_, _, err := c.fetchTargetGraphs(t.Context(), c.emitter, c.logger, bypassRequest())
		require.Error(t, err)
	})
}

func TestToDiffGraph_SkipsUnresolvedIDs(t *testing.T) {
	targetsByID := map[int32]*entity.OptimizedTarget{
		1: {
			Hash:               "h1",
			RuleType:           100,
			DirectDependencies: []int32{2, 99},
			Tags:               []int32{10, 88},
			Attributes:         map[int32]int32{20: 30, 77: 30},
		},
		2: {Hash: "h2", RuleType: 100},
		3: {Hash: "h3", RuleType: 100},
	}
	meta := &entity.Metadata{
		TargetIDMapping:             map[int32]string{1: "//app:a", 2: "//app:b"},
		RuleTypeMapping:             map[int32]string{100: "go_library"},
		TagMapping:                  map[int32]string{10: "tag_a"},
		AttributeNameMapping:        map[int32]string{20: "attr_a"},
		AttributeStringValueMapping: map[int32]string{30: "val_a"},
	}

	graph, err := toDiffGraph(t.Context(), targetsByID, meta, nil)
	require.NoError(t, err)

	require.Len(t, graph, 2)
	require.NotContains(t, graph, "")

	a := graph["//app:a"]
	require.NotNil(t, a)
	assert.Equal(t, "go_library", a.RuleType)
	assert.Equal(t, []string{"//app:b"}, a.Dependencies)
	assert.Equal(t, []string{"tag_a"}, a.Tags)
	assert.Equal(t, map[string]string{"attr_a": "val_a"}, a.Attributes)

	_, ok := graph["//app:b"]
	assert.True(t, ok)
}

func TestToDiffGraph_OnlyKeepsAllowlistedAttributes(t *testing.T) {
	targetsByID := map[int32]*entity.OptimizedTarget{
		1: {
			Hash:     "h1",
			RuleType: 100,
			Attributes: map[int32]int32{
				20: 30, // size (allowlisted)
				21: 31, // generator_location (cosmetic BUILD-file position, not allowlisted)
				22: 32, // package_name (not allowlisted)
			},
		},
	}
	meta := &entity.Metadata{
		TargetIDMapping: map[int32]string{1: "//app:a"},
		RuleTypeMapping: map[int32]string{100: "go_library"},
		AttributeNameMapping: map[int32]string{
			20: "size",
			21: "generator_location",
			22: "package_name",
		},
		AttributeStringValueMapping: map[int32]string{
			30: "val_a",
			31: "app/BUILD.bazel:12:1",
			32: "app",
		},
	}

	graph, err := toDiffGraph(t.Context(), targetsByID, meta, map[string]bool{"size": true})
	require.NoError(t, err)

	a := graph["//app:a"]
	require.NotNil(t, a)
	assert.Equal(t, map[string]string{"size": "val_a"}, a.Attributes)
}

// fakeRepoConfigProvider is a minimal RepositoryConfigProvider test double;
// the interface is small enough not to warrant a generated mock.
type fakeRepoConfigProvider map[string]config.RepositoryConfig

func (f fakeRepoConfigProvider) GetRepositoryConfig(remote string) (config.RepositoryConfig, bool) {
	cfg, ok := f[remote]
	return cfg, ok
}

func TestSeedAttributesFor(t *testing.T) {
	t.Run("no repo config provider means no filtering", func(t *testing.T) {
		c := newTestController(zaptest.NewLogger(t))
		assert.Nil(t, c.seedAttributesFor("some-remote"))
	})

	t.Run("remote not found means no filtering", func(t *testing.T) {
		c := newTestController(zaptest.NewLogger(t))
		c.repoConfig = fakeRepoConfigProvider{}
		assert.Nil(t, c.seedAttributesFor("some-remote"))
	})

	t.Run("configured remote with no seed_attributes means no filtering", func(t *testing.T) {
		c := newTestController(zaptest.NewLogger(t))
		c.repoConfig = fakeRepoConfigProvider{
			"some-remote": config.RepositoryConfig{Remote: "some-remote"},
		}
		assert.Nil(t, c.seedAttributesFor("some-remote"))
	})

	t.Run("configured remote with seed_attributes returns allowlist", func(t *testing.T) {
		c := newTestController(zaptest.NewLogger(t))
		c.repoConfig = fakeRepoConfigProvider{
			"some-remote": config.RepositoryConfig{
				Remote:         "some-remote",
				SeedAttributes: []string{"size", "timeout"},
			},
		}
		assert.Equal(t, map[string]bool{"size": true, "timeout": true}, c.seedAttributesFor("some-remote"))
	})
}
