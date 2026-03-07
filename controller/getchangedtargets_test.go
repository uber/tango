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
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	gogio "github.com/gogo/protobuf/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/storage"
	storagemock "github.com/uber/tango/core/storage/storagemock"
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
				SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing second revision",
			request: &pb.GetChangedTargetsRequest{
				FirstRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
			},
			wantErr: true,
		},
		{
			name: "missing first revision remote",
			request: &pb.GetChangedTargetsRequest{
				FirstRevision:  &pb.BuildDescription{BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing first revision base_sha",
			request: &pb.GetChangedTargetsRequest{
				FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code"},
				SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing second revision remote",
			request: &pb.GetChangedTargetsRequest{
				FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing second revision base_sha",
			request: &pb.GetChangedTargetsRequest{
				FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{Remote: "repo:go-code"},
			},
			wantErr: true,
		},
		{
			name: "different remotes",
			request: &pb.GetChangedTargetsRequest{
				FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{Remote: "repo:other", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "valid request",
			request: &pb.GetChangedTargetsRequest{
				FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
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

	firstGraph := &pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Metadata{
			Metadata: &pb.Metadata{},
		},
	}
	secondGraph := &pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Metadata{
			Metadata: &pb.Metadata{},
		},
	}

	response, err := c.compareTargetGraphs(context.Background(), zap.NewNop(), []*pb.GetTargetGraphResponse{firstGraph}, []*pb.GetTargetGraphResponse{secondGraph}, -1, false)
	require.NoError(t, err)
	require.NotNil(t, response)
}

func TestGetChangedTargets_ValidationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)

	c := NewController(Params{Logger: zap.NewNop(), Orchestrator: orchestratormock.NewMockOrchestrator(ctrl)})

	err := c.GetChangedTargets(nil, stream)
	assert.EqualError(t, err, "request cannot be nil")
}

func TestGetChangedTargets_CacheHit(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())

	// Build a cached response with one ChangedTargets message and one Metadata message.
	cachedChanged := &pb.GetChangedTargetsResponse{
		Item: &pb.GetChangedTargetsResponse_ChangedTargets{
			ChangedTargets: &pb.ChangedTargets{},
		},
	}
	cachedMeta := &pb.GetChangedTargetsResponse{
		Item: &pb.GetChangedTargetsResponse_Metadata{
			Metadata: &pb.Metadata{},
		},
	}
	var buf bytes.Buffer
	w := gogio.NewDelimitedWriter(&buf)
	w.WriteMsg(cachedChanged)
	w.WriteMsg(cachedMeta)
	cachedBytes := buf.Bytes()

	storagemock := storagemock.NewMockStorage(ctrl)
	// First two Gets resolve the treehashes, third gets the cached comparison result.
	gomock.InOrder(
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(&storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash1")))}, nil),
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(&storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash2")))}, nil),
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(&storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(cachedBytes))}, nil),
	)

	stream.EXPECT().Send(gomock.Any()).Return(nil).Times(2)

	c := NewController(Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      storagemock,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	request := &pb.GetChangedTargetsRequest{
		FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
	}

	err := c.GetChangedTargets(request, stream)
	require.NoError(t, err)
}

func TestGetChangedTargets_GetGraphError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())

	storagemock := storagemock.NewMockStorage(ctrl)
	// First two Gets are treehash pre-reads (both return error -> treated as cache miss, skip
	// comparison cache check). The next two are the goroutine treehash lookups, which also fail
	// with a non-NotFound error so getGraph propagates the error for both revisions.
	storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).
		Return(nil, errors.New("graph error")).Times(4)

	c := NewController(Params{
		Logger:       zap.NewNop(),
		Storage:      storagemock,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	request := &pb.GetChangedTargetsRequest{
		FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
	}

	err := c.GetChangedTargets(request, stream)
	assert.Error(t, err)
}

func TestGetChangedTargets_StreamSendError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())

	stream.EXPECT().Send(gomock.Any()).Return(errors.New("send error"))
	storagemock := storagemock.NewMockStorage(ctrl)

	var buf bytes.Buffer
	gogio.NewDelimitedWriter(&buf).WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Targets{Targets: &pb.OptimizedTargets{}},
	})
	storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, req storage.DownloadRequest) (*storage.DownloadResponse, error) {
		if strings.Contains(req.Key, "compared-targets") {
			return nil, &storage.NotFoundError{Path: req.Key}
		}
		if strings.Contains(req.Key, "th") {
			return &storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(buf.Bytes()))}, nil
		}
		return &storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("th")))}, nil
	}).AnyTimes()

	// Put is launched in a goroutine — use a channel to wait for it before the test ends.
	putDone := make(chan struct{}, 1)
	storagemock.EXPECT().Put(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ storage.UploadRequest) error {
		putDone <- struct{}{}
		return nil
	})

	c := NewController(Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      storagemock,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	request := &pb.GetChangedTargetsRequest{
		FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
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
	stream.EXPECT().Context().Return(context.Background())

	var sentResponses []*pb.GetChangedTargetsResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(resp *pb.GetChangedTargetsResponse, opts ...interface{}) error {
		sentResponses = append(sentResponses, resp)
		return nil
	}).Times(2)

	storagemock := storagemock.NewMockStorage(ctrl)

	// Build first revision graph (2 chunks: Targets + Metadata)
	var buf1 bytes.Buffer
	w1 := gogio.NewDelimitedWriter(&buf1)
	w1.WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Targets{
			Targets: &pb.OptimizedTargets{
				Targets: []*pb.OptimizedTarget{
					{Id: 1, Hash: "h1", RuleType: 100},
					{Id: 2, Hash: "h2-old", RuleType: 200},
				},
			},
		},
	})
	w1.WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Metadata{
			Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{1: "//app:target1", 2: "//app:target2"},
				RuleTypeMapping: map[int32]string{100: "go_library", 200: "go_binary"},
			},
		},
	})
	graph1Bytes := buf1.Bytes()

	// Build second revision graph - target2 has different hash
	var buf2 bytes.Buffer
	w2 := gogio.NewDelimitedWriter(&buf2)
	w2.WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Targets{
			Targets: &pb.OptimizedTargets{
				Targets: []*pb.OptimizedTarget{
					{Id: 1, Hash: "h1", RuleType: 100},
					{Id: 2, Hash: "h2-new", RuleType: 200}, // changed hash
				},
			},
		},
	})
	w2.WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Metadata{
			Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{1: "//app:target1", 2: "//app:target2"},
				RuleTypeMapping: map[int32]string{100: "go_library", 200: "go_binary"},
			},
		},
	})
	graph2Bytes := buf2.Bytes()

	// Each revision needs: treehash lookup + graph lookup. Plus one initial cache miss.
	storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req storage.DownloadRequest) (*storage.DownloadResponse, error) {
			switch {
			case strings.Contains(req.Key, "compared-targets"):
				return nil, &storage.NotFoundError{Path: req.Key}
			case strings.Contains(req.Key, "sha1"):
				return &storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash1")))}, nil
			case strings.Contains(req.Key, "sha2"):
				return &storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash2")))}, nil
			case strings.Contains(req.Key, "treehash1"):
				return &storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(graph1Bytes))}, nil
			case strings.Contains(req.Key, "treehash2"):
				return &storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(graph2Bytes))}, nil
			default:
				return nil, fmt.Errorf("unexpected key: %s", req.Key)
			}
			// readTreehash (×2 pre) + comparison cache miss (×1) + graph computation (×4) + readTreehash (×2 post) = 9
		}).Times(9)
	// Put is launched in a goroutine — use a channel to wait for it before the test ends.
	putDone := make(chan struct{}, 1)
	storagemock.EXPECT().Put(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ storage.UploadRequest) error {
		putDone <- struct{}{}
		return nil
	})

	c := NewController(Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      storagemock,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	request := &pb.GetChangedTargetsRequest{
		FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
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

func TestCompareTargetGraphs_NewTarget_CanonicalIDs(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	first := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Metadata{
				Metadata: &pb.Metadata{
					TargetIdMapping:             map[int32]string{},
					RuleTypeMapping:             map[int32]string{},
					TagMapping:                  map[int32]string{},
					AttributeNameMapping:        map[int32]string{},
					AttributeStringValueMapping: map[int32]string{},
				},
			},
		},
	}
	second := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 10, Hash: "h2", RuleType: 1},
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{
				Metadata: &pb.Metadata{
					TargetIdMapping:             map[int32]string{10: "//app:new"},
					RuleTypeMapping:             map[int32]string{1: "rule"},
					TagMapping:                  map[int32]string{},
					AttributeNameMapping:        map[int32]string{},
					AttributeStringValueMapping: map[int32]string{},
				},
			},
		},
	}
	res, err := c.compareTargetGraphs(context.Background(), zap.NewNop(), first, second, -1, false)
	require.NoError(t, err)
	require.Len(t, res, 2)
	cs := res[0].GetChangedTargets()
	require.NotNil(t, cs)
	require.Len(t, cs.GetChangedTargets(), 1)
	ct := cs.GetChangedTargets()[0]
	require.Equal(t, pb.CHANGE_TYPE_NEW, ct.GetChangeType())
	// ID used in target should match canonical metadata mapping
	meta := res[1].GetMetadata()
	require.NotNil(t, meta)
	newID := ct.GetNewTarget().GetId()
	require.Equal(t, "//app:new", meta.GetTargetIdMapping()[newID])
}

func TestCompareTargetGraphs_SourceFileDirectAndPropagation(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Old: source file A (id 1, hash h1), lib L (id 2, hash h1, dep -> A)
	first := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 1, Hash: "h1", RuleType: 100},                                 // "source file"
						{Id: 2, Hash: "h1", RuleType: 200, DirectDependencies: []int32{1}}, // "rule"
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{
				Metadata: &pb.Metadata{
					TargetIdMapping: map[int32]string{
						1: "//app:A",
						2: "//app:L",
					},
					RuleTypeMapping: map[int32]string{
						100: "source file",
						200: "rule",
					},
				},
			},
		},
	}
	// New: both change hashes; same structure
	second := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 11, Hash: "h2", RuleType: 101},                                  // "source file"
						{Id: 22, Hash: "h2", RuleType: 201, DirectDependencies: []int32{11}}, // "rule"
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{
				Metadata: &pb.Metadata{
					TargetIdMapping: map[int32]string{
						11: "//app:A",
						22: "//app:L",
					},
					RuleTypeMapping: map[int32]string{
						101: "source file",
						201: "rule",
					},
				},
			},
		},
	}
	res, err := c.compareTargetGraphs(context.Background(), zap.NewNop(), first, second, -1, false)
	require.NoError(t, err)
	cs := res[0].GetChangedTargets()
	require.NotNil(t, cs)
	// Expect 2 changed: A (DIRECT) and L (DIRECT due to dep on changed source)
	require.Len(t, cs.GetChangedTargets(), 2)
	var aCT, lCT *pb.ChangedTarget
	for _, ct := range cs.GetChangedTargets() {
		name := res[1].GetMetadata().GetTargetIdMapping()[ct.GetNewTarget().GetId()]
		if name == "//app:A" {
			aCT = ct
		}
		if name == "//app:L" {
			lCT = ct
		}
	}
	require.NotNil(t, aCT)
	require.NotNil(t, lCT)
	require.Equal(t, pb.CHANGE_TYPE_DIRECT, aCT.GetChangeType())
	require.Equal(t, pb.CHANGE_TYPE_DIRECT, lCT.GetChangeType())
	// Old and new IDs must match for each changed target under canonical metadata
	require.Equal(t, aCT.GetOldTarget().GetId(), aCT.GetNewTarget().GetId())
	require.Equal(t, lCT.GetOldTarget().GetId(), lCT.GetNewTarget().GetId())
}

func TestCompareTargetGraphs_IndirectWhenNoSourceDep(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Old: T (id 1, rule), no deps
	first := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 1, Hash: "h1", RuleType: 200},
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{
				Metadata: &pb.Metadata{
					TargetIdMapping: map[int32]string{1: "//app:T"},
					RuleTypeMapping: map[int32]string{100: "source file", 200: "rule"},
				},
			},
		},
	}
	// New: T hash changed, still no deps
	second := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 2, Hash: "h2", RuleType: 201},
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{
				Metadata: &pb.Metadata{
					TargetIdMapping: map[int32]string{2: "//app:T"},
					RuleTypeMapping: map[int32]string{101: "source file", 201: "rule"},
				},
			},
		},
	}
	res, err := c.compareTargetGraphs(context.Background(), zap.NewNop(), first, second, -1, false)
	require.NoError(t, err)
	cs := res[0].GetChangedTargets()
	require.NotNil(t, cs)
	require.Len(t, cs.GetChangedTargets(), 1)
	require.Equal(t, pb.CHANGE_TYPE_INDIRECT, cs.GetChangedTargets()[0].GetChangeType())
}

func TestCompareTargetGraphs_DirectWhenDependenciesChanged(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Old: T (id 1, rule) with deps on A
	first := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 1, Hash: "h1", RuleType: 200, DirectDependencies: []int32{10}},
						{Id: 10, Hash: "h1", RuleType: 200}, // Dependency A
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{
				Metadata: &pb.Metadata{
					TargetIdMapping: map[int32]string{
						1:  "//app:T",
						10: "//app:A",
					},
					RuleTypeMapping: map[int32]string{
						100: "source file",
						200: "rule",
					},
				},
			},
		},
	}
	// New: T now depends on B instead of A (hash changed due to dep change)
	second := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 2, Hash: "h2", RuleType: 201, DirectDependencies: []int32{20}},
						{Id: 20, Hash: "h1", RuleType: 201}, // Dependency B
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{
				Metadata: &pb.Metadata{
					TargetIdMapping: map[int32]string{
						2:  "//app:T",
						20: "//app:B",
					},
					RuleTypeMapping: map[int32]string{
						101: "source file",
						201: "rule",
					},
				},
			},
		},
	}
	res, err := c.compareTargetGraphs(context.Background(), zap.NewNop(), first, second, -1, false)
	require.NoError(t, err)
	cs := res[0].GetChangedTargets()
	require.NotNil(t, cs)

	// Find target T in the changed targets
	var targetT *pb.ChangedTarget
	for _, ct := range cs.GetChangedTargets() {
		name := res[1].GetMetadata().GetTargetIdMapping()[ct.GetNewTarget().GetId()]
		if name == "//app:T" {
			targetT = ct
			break
		}
	}
	require.NotNil(t, targetT)
	require.Equal(t, pb.CHANGE_TYPE_DIRECT, targetT.GetChangeType(), "Target with changed dependencies should be marked as DIRECT")
}

func TestCompareTargetGraphs_DirectWhenAttributesChanged(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Old: T with attribute "key1" -> "value1"
	first := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{
							Id:         1,
							Hash:       "h1",
							RuleType:   200,
							Attributes: map[int32]int32{1: 10}, // attr name 1 -> attr value 10
						},
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{
				Metadata: &pb.Metadata{
					TargetIdMapping: map[int32]string{1: "//app:T"},
					RuleTypeMapping: map[int32]string{
						100: "source file",
						200: "rule",
					},
					AttributeNameMapping:        map[int32]string{1: "key1"},
					AttributeStringValueMapping: map[int32]string{10: "value1"},
				},
			},
		},
	}
	// New: T with attribute "key1" -> "value2" (changed value)
	second := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{
							Id:         2,
							Hash:       "h2",
							RuleType:   201,
							Attributes: map[int32]int32{2: 20}, // attr name 2 -> attr value 20
						},
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{
				Metadata: &pb.Metadata{
					TargetIdMapping: map[int32]string{2: "//app:T"},
					RuleTypeMapping: map[int32]string{
						101: "source file",
						201: "rule",
					},
					AttributeNameMapping:        map[int32]string{2: "key1"},
					AttributeStringValueMapping: map[int32]string{20: "value2"},
				},
			},
		},
	}
	res, err := c.compareTargetGraphs(context.Background(), zap.NewNop(), first, second, -1, false)
	require.NoError(t, err)
	cs := res[0].GetChangedTargets()
	require.NotNil(t, cs)
	require.Len(t, cs.GetChangedTargets(), 1)
	require.Equal(t, pb.CHANGE_TYPE_DIRECT, cs.GetChangedTargets()[0].GetChangeType(), "Target with changed attributes should be marked as DIRECT")
}

func TestCompareTargetGraphs_DirectWhenNewAttributeAdded(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Old: T with one attribute
	first := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{
							Id:         1,
							Hash:       "h1",
							RuleType:   200,
							Attributes: map[int32]int32{1: 10},
						},
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{
				Metadata: &pb.Metadata{
					TargetIdMapping: map[int32]string{1: "//app:T"},
					RuleTypeMapping: map[int32]string{
						100: "source file",
						200: "rule",
					},
					AttributeNameMapping:        map[int32]string{1: "key1"},
					AttributeStringValueMapping: map[int32]string{10: "value1"},
				},
			},
		},
	}
	// New: T with two attributes (added key2)
	second := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{
							Id:       2,
							Hash:     "h2",
							RuleType: 201,
							Attributes: map[int32]int32{
								2: 20, // key1 -> value1
								3: 30, // key2 -> value2 (NEW)
							},
						},
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{
				Metadata: &pb.Metadata{
					TargetIdMapping: map[int32]string{2: "//app:T"},
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
		},
	}
	res, err := c.compareTargetGraphs(context.Background(), zap.NewNop(), first, second, -1, false)
	require.NoError(t, err)
	cs := res[0].GetChangedTargets()
	require.NotNil(t, cs)
	require.Len(t, cs.GetChangedTargets(), 1)
	require.Equal(t, pb.CHANGE_TYPE_DIRECT, cs.GetChangedTargets()[0].GetChangeType(), "Target with new attribute added should be marked as DIRECT")
}

func TestComputeDistances(t *testing.T) {
	// Graph:
	//   A (DIRECT)  <-  B (INDIRECT)  <-  C (INDIRECT)
	//   D (DIRECT)  <---------------------┘
	//   E (INDIRECT, no deps — unreachable)
	//
	// Expected distances:
	//   A=0  B=1  C=1  D=0  E=-1

	meta := &pb.Metadata{
		TargetIdMapping: map[int32]string{
			1: "A", 2: "B", 3: "C", 4: "D", 5: "E",
		},
	}

	targetsByName := map[string]*pb.OptimizedTarget{
		"A": {Id: 1},
		"B": {Id: 2, DirectDependencies: []int32{1}},    // [A]
		"C": {Id: 3, DirectDependencies: []int32{2, 4}}, // [B, D]
		"D": {Id: 4},
		"E": {Id: 5},
	}

	changedByName := map[string]*pb.ChangedTarget{
		"A": {ChangeType: pb.CHANGE_TYPE_DIRECT},
		"B": {ChangeType: pb.CHANGE_TYPE_INDIRECT},
		"C": {ChangeType: pb.CHANGE_TYPE_INDIRECT},
		"D": {ChangeType: pb.CHANGE_TYPE_DIRECT},
		"E": {ChangeType: pb.CHANGE_TYPE_INDIRECT},
	}

	computeDistances(zap.NewNop(), changedByName, targetsByName, meta, -1)

	assert.Equal(t, int32(0), changedByName["A"].GetDistance(), "DIRECT target A should have distance 0")
	assert.Equal(t, int32(1), changedByName["B"].GetDistance(), "B depends on DIRECT A, distance should be 1")
	assert.Equal(t, int32(1), changedByName["C"].GetDistance(), "C depends on DIRECT D (shorter than 2 via A→B), distance should be 1")
	assert.Equal(t, int32(0), changedByName["D"].GetDistance(), "DIRECT target D should have distance 0")
	assert.Equal(t, int32(-1), changedByName["E"].GetDistance(), "E has no path to any DIRECT target, distance should be -1")
}

func TestSendWithDistanceFilter_MetadataAlwaysForwarded(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)

	meta := &pb.Metadata{TargetIdMapping: map[int32]string{1: "//app:T"}}
	responses := []*pb.GetChangedTargetsResponse{
		{
			Item: &pb.GetChangedTargetsResponse_ChangedTargets{
				ChangedTargets: &pb.ChangedTargets{
					ChangedTargets: []*pb.ChangedTarget{
						{Distance: 5, ChangeType: pb.CHANGE_TYPE_INDIRECT},
					},
				},
			},
		},
		{
			Item: &pb.GetChangedTargetsResponse_Metadata{Metadata: meta},
		},
	}

	var sent []*pb.GetChangedTargetsResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(r *pb.GetChangedTargetsResponse, _ ...any) error {
		sent = append(sent, r)
		return nil
	}).Times(2)

	// max_distance=1 filters out the distance-5 target, metadata always forwarded
	require.NoError(t, sendWithDistanceFilter(stream, responses, 1))

	// First response: target filtered out (distance 5 > maxDist 1)
	assert.Empty(t, sent[0].GetChangedTargets().GetChangedTargets())
	// Second response: metadata always forwarded
	assert.Equal(t, meta, sent[1].GetMetadata())
}

func TestSendWithDistanceFilter_SendError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)

	responses := []*pb.GetChangedTargetsResponse{
		{
			Item: &pb.GetChangedTargetsResponse_ChangedTargets{
				ChangedTargets: &pb.ChangedTargets{},
			},
		},
	}

	stream.EXPECT().Send(gomock.Any()).Return(errors.New("send error"))

	err := sendWithDistanceFilter(stream, responses, -1)
	assert.EqualError(t, err, "send error")
}

func TestGetChangedTargets_CacheHitWithDistanceFilter(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())

	// Cached response: two targets at distances 0 and 2, plus metadata.
	cachedChanged := &pb.GetChangedTargetsResponse{
		Item: &pb.GetChangedTargetsResponse_ChangedTargets{
			ChangedTargets: &pb.ChangedTargets{
				ChangedTargets: []*pb.ChangedTarget{
					{Distance: 0, ChangeType: pb.CHANGE_TYPE_DIRECT},
					{Distance: 2, ChangeType: pb.CHANGE_TYPE_INDIRECT},
				},
			},
		},
	}
	cachedMeta := &pb.GetChangedTargetsResponse{
		Item: &pb.GetChangedTargetsResponse_Metadata{Metadata: &pb.Metadata{}},
	}
	var buf bytes.Buffer
	w := gogio.NewDelimitedWriter(&buf)
	w.WriteMsg(cachedChanged)
	w.WriteMsg(cachedMeta)
	cachedBytes := buf.Bytes()

	storagemock := storagemock.NewMockStorage(ctrl)
	gomock.InOrder(
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(&storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash1")))}, nil),
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(&storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash2")))}, nil),
		storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(&storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(cachedBytes))}, nil),
	)

	var sent []*pb.GetChangedTargetsResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(r *pb.GetChangedTargetsResponse, _ ...any) error {
		sent = append(sent, r)
		return nil
	}).Times(2)

	c := NewController(Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      storagemock,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	request := &pb.GetChangedTargetsRequest{
		FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
		OutputConfig:   &pb.OutputConfig{ComputeDistances: true, MaxDistance: 1},
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

func TestComputeDistances_NewTargetsGetDistanceZero(t *testing.T) {
	// Graph:
	//   A (DIRECT)  <-  B (INDIRECT)
	//   N (NEW, no deps)
	//
	// NEW targets should be treated like DIRECT: distance 0, and seed BFS.

	meta := &pb.Metadata{
		TargetIdMapping: map[int32]string{
			1: "A", 2: "B", 3: "N",
		},
	}

	targetsByName := map[string]*pb.OptimizedTarget{
		"A": {Id: 1},
		"B": {Id: 2, DirectDependencies: []int32{1}},
		"N": {Id: 3},
	}

	changedByName := map[string]*pb.ChangedTarget{
		"A": {ChangeType: pb.CHANGE_TYPE_DIRECT},
		"B": {ChangeType: pb.CHANGE_TYPE_INDIRECT},
		"N": {ChangeType: pb.CHANGE_TYPE_NEW},
	}

	computeDistances(zap.NewNop(), changedByName, targetsByName, meta, -1)

	assert.Equal(t, int32(0), changedByName["A"].GetDistance(), "DIRECT target A should have distance 0")
	assert.Equal(t, int32(1), changedByName["B"].GetDistance(), "B depends on DIRECT A, distance should be 1")
	assert.Equal(t, int32(0), changedByName["N"].GetDistance(), "NEW target N should have distance 0")
}

func TestComputeDistances_NewTargetsWithMaxDistance(t *testing.T) {
	// When maxDistance is set, NEW targets should still get distance 0
	// and not be filtered out.

	meta := &pb.Metadata{
		TargetIdMapping: map[int32]string{1: "N"},
	}

	targetsByName := map[string]*pb.OptimizedTarget{
		"N": {Id: 1},
	}

	changedByName := map[string]*pb.ChangedTarget{
		"N": {ChangeType: pb.CHANGE_TYPE_NEW},
	}

	computeDistances(zap.NewNop(), changedByName, targetsByName, meta, 1)

	assert.Equal(t, int32(0), changedByName["N"].GetDistance(), "NEW target should have distance 0 even with maxDistance set")
}

func TestComputeDistances_NilMetadata(t *testing.T) {
	changedByName := map[string]*pb.ChangedTarget{
		"A": {ChangeType: pb.CHANGE_TYPE_DIRECT},
	}
	computeDistances(zap.NewNop(), changedByName, nil, nil, -1)
	assert.Equal(t, int32(0), changedByName["A"].GetDistance())
}

func TestCompareTargetGraphs_IndirectWhenOnlyHashChanged(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Old: T with deps and attributes
	first := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{
							Id:                 1,
							Hash:               "h1",
							RuleType:           200,
							DirectDependencies: []int32{10},
							Attributes:         map[int32]int32{1: 10},
						},
						{Id: 10, Hash: "h1", RuleType: 200},
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{
				Metadata: &pb.Metadata{
					TargetIdMapping: map[int32]string{
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
		},
	}
	// New: T with same deps and attributes, but hash changed (e.g., due to transitive dep change)
	second := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{
							Id:                 2,
							Hash:               "h2", // Changed
							RuleType:           201,
							DirectDependencies: []int32{20},            // Same dep (//app:A)
							Attributes:         map[int32]int32{2: 20}, // Same attribute
						},
						{Id: 20, Hash: "h2", RuleType: 201}, // Dep A hash changed
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{
				Metadata: &pb.Metadata{
					TargetIdMapping: map[int32]string{
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
		},
	}
	res, err := c.compareTargetGraphs(context.Background(), zap.NewNop(), first, second, -1, false)
	require.NoError(t, err)
	cs := res[0].GetChangedTargets()
	require.NotNil(t, cs)

	// Find target T
	var targetT *pb.ChangedTarget
	for _, ct := range cs.GetChangedTargets() {
		name := res[1].GetMetadata().GetTargetIdMapping()[ct.GetNewTarget().GetId()]
		if name == "//app:T" {
			targetT = ct
			break
		}
	}
	require.NotNil(t, targetT)
	require.Equal(t, pb.CHANGE_TYPE_INDIRECT, targetT.GetChangeType(), "Target with only hash change (not deps/attrs) should be marked as INDIRECT")
}
