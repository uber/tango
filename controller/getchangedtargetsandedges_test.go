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

// collectCTEResponses merges all chunked GetChangedTargetsAndEdgesResponse messages into
// a single ChangedTargetsAndEdges and Metadata, matching the original single-message shape
// that tests were written against.
func collectCTEResponses(responses []*pb.GetChangedTargetsAndEdgesResponse) (*pb.ChangedTargetsAndEdges, *pb.Metadata) {
	cte := &pb.ChangedTargetsAndEdges{}
	meta := &pb.Metadata{
		TargetIdMapping:             make(map[int32]string),
		RuleTypeMapping:             make(map[int32]string),
		TagMapping:                  make(map[int32]string),
		AttributeNameMapping:        make(map[int32]string),
		AttributeStringValueMapping: make(map[int32]string),
	}
	for _, resp := range responses {
		switch item := resp.GetItem().(type) {
		case *pb.GetChangedTargetsAndEdgesResponse_ChangedTargetsAndEdges:
			c := item.ChangedTargetsAndEdges
			cte.ChangedTargets = append(cte.ChangedTargets, c.GetChangedTargets()...)
			cte.AddedTargets = append(cte.AddedTargets, c.GetAddedTargets()...)
			cte.RemovedTargets = append(cte.RemovedTargets, c.GetRemovedTargets()...)
			cte.NewEdges = append(cte.NewEdges, c.GetNewEdges()...)
			cte.RemovedEdges = append(cte.RemovedEdges, c.GetRemovedEdges()...)
		case *pb.GetChangedTargetsAndEdgesResponse_Metadata:
			m := item.Metadata
			for k, v := range m.GetTargetIdMapping() {
				meta.TargetIdMapping[k] = v
			}
			for k, v := range m.GetRuleTypeMapping() {
				meta.RuleTypeMapping[k] = v
			}
			for k, v := range m.GetTagMapping() {
				meta.TagMapping[k] = v
			}
			for k, v := range m.GetAttributeNameMapping() {
				meta.AttributeNameMapping[k] = v
			}
			for k, v := range m.GetAttributeStringValueMapping() {
				meta.AttributeStringValueMapping[k] = v
			}
		}
	}
	return cte, meta
}

// --- Validation ---

func TestValidateGetChangedTargetsAndEdgesRequest(t *testing.T) {
	tests := []struct {
		name    string
		request *pb.GetChangedTargetsAndEdgesRequest
		wantErr bool
	}{
		{name: "nil request", request: nil, wantErr: true},
		{
			name: "missing first revision",
			request: &pb.GetChangedTargetsAndEdgesRequest{
				SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing second revision",
			request: &pb.GetChangedTargetsAndEdgesRequest{
				FirstRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
			},
			wantErr: true,
		},
		{
			name: "missing first revision remote",
			request: &pb.GetChangedTargetsAndEdgesRequest{
				FirstRevision:  &pb.BuildDescription{BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing first revision base_sha",
			request: &pb.GetChangedTargetsAndEdgesRequest{
				FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code"},
				SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing second revision remote",
			request: &pb.GetChangedTargetsAndEdgesRequest{
				FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing second revision base_sha",
			request: &pb.GetChangedTargetsAndEdgesRequest{
				FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{Remote: "repo:go-code"},
			},
			wantErr: true,
		},
		{
			name: "different remotes",
			request: &pb.GetChangedTargetsAndEdgesRequest{
				FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{Remote: "repo:other", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "valid request",
			request: &pb.GetChangedTargetsAndEdgesRequest{
				FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGetChangedTargetsAndEdgesRequest(tt.request)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// --- GetChangedTargetsAndEdges handler tests ---

func TestGetChangedTargetsAndEdges_ValidationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsAndEdgesYARPCServer(ctrl)

	c := NewController(Params{Logger: zap.NewNop(), Orchestrator: orchestratormock.NewMockOrchestrator(ctrl)})

	err := c.GetChangedTargetsAndEdges(nil, stream)
	assert.EqualError(t, err, "request cannot be nil")
}

func TestGetChangedTargetsAndEdges_GetGraphError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsAndEdgesYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())

	store := storagemock.NewMockStorage(ctrl)
	// 2 treehash pre-reads (cache bypass check) + 2 getGraph treehash lookups = 4 total.
	store.EXPECT().Get(gomock.Any(), gomock.Any()).
		Return(nil, errors.New("storage error")).Times(4)

	c := NewController(Params{
		Logger:       zap.NewNop(),
		Storage:      store,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	err := c.GetChangedTargetsAndEdges(&pb.GetChangedTargetsAndEdgesRequest{
		FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
	}, stream)
	assert.Error(t, err)
}

func TestGetChangedTargetsAndEdges_StreamSendError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsAndEdgesYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())
	stream.EXPECT().Send(gomock.Any()).Return(errors.New("send error"))

	store := storagemock.NewMockStorage(ctrl)
	store.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req storage.DownloadRequest) (*storage.DownloadResponse, error) {
			var buf bytes.Buffer
			gogio.NewDelimitedWriter(&buf).WriteMsg(&pb.GetTargetGraphResponse{
				Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{}},
			})
			switch {
			case strings.Contains(req.Key, "compared-targets-and-edges"):
				return nil, &storage.NotFoundError{Path: req.Key}
			case strings.Contains(req.Key, "sha1") || strings.Contains(req.Key, "sha2"):
				return &storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("th")))}, nil
			default:
				return &storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(buf.Bytes()))}, nil
			}
		}).AnyTimes()

	putDone := make(chan struct{}, 1)
	store.EXPECT().Put(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ storage.UploadRequest) error {
		putDone <- struct{}{}
		return nil
	})

	c := NewController(Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      store,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	err := c.GetChangedTargetsAndEdges(&pb.GetChangedTargetsAndEdgesRequest{
		FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
	}, stream)
	assert.Error(t, err)

	select {
	case <-putDone:
	case <-time.After(time.Second):
		assert.Fail(t, "cache write goroutine did not complete in time")
	}
}

// --- compareTargetGraphsAndEdges tests ---

func TestCompareTargetGraphsAndEdges_Empty(t *testing.T) {
	c := newTestController(zap.NewNop())

	emptyGraph := func() []*pb.GetTargetGraphResponse {
		return []*pb.GetTargetGraphResponse{{
			Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{}},
		}}
	}

	res, err := c.compareTargetGraphsAndEdges(zap.NewNop(), emptyGraph(), emptyGraph(), -1, false)
	require.NoError(t, err)
	require.Len(t, res, 2)
	cte, _ := collectCTEResponses(res)
	require.NotNil(t, cte)
	assert.Empty(t, cte.GetChangedTargets())
	assert.Empty(t, cte.GetAddedTargets())
	assert.Empty(t, cte.GetRemovedTargets())
	assert.Empty(t, cte.GetNewEdges())
	assert.Empty(t, cte.GetRemovedEdges())
	_, meta := collectCTEResponses(res)
	assert.NotNil(t, meta)
}

func TestCompareTargetGraphsAndEdges_AddedTarget(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// First graph: empty
	first := []*pb.GetTargetGraphResponse{{
		Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{
			TargetIdMapping: map[int32]string{},
		}},
	}}
	// Second graph: one new target
	second := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{{Id: 10, Hash: "h1", RuleType: 1}},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{10: "//app:new"},
				RuleTypeMapping: map[int32]string{1: "go_library"},
			}},
		},
	}

	res, err := c.compareTargetGraphsAndEdges(zap.NewNop(), first, second, -1, false)
	require.NoError(t, err)
	cte, meta := collectCTEResponses(res)

	require.Len(t, cte.GetChangedTargets(), 1)
	assert.Equal(t, pb.CHANGE_TYPE_NEW, cte.GetChangedTargets()[0].GetChangeType())

	require.Len(t, cte.GetAddedTargets(), 1)
	addedID := cte.GetAddedTargets()[0].GetId()
	assert.Equal(t, "//app:new", meta.GetTargetIdMapping()[addedID])

	assert.Empty(t, cte.GetRemovedTargets())
	assert.Empty(t, cte.GetNewEdges())
	assert.Empty(t, cte.GetRemovedEdges())
}

func TestCompareTargetGraphsAndEdges_RemovedTarget(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// First graph: one target
	first := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{{Id: 1, Hash: "h1", RuleType: 10}},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{1: "//app:gone"},
				RuleTypeMapping: map[int32]string{10: "go_library"},
			}},
		},
	}
	// Second graph: empty
	second := []*pb.GetTargetGraphResponse{{
		Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{
			TargetIdMapping: map[int32]string{},
		}},
	}}

	res, err := c.compareTargetGraphsAndEdges(zap.NewNop(), first, second, -1, false)
	require.NoError(t, err)
	cte, meta := collectCTEResponses(res)

	assert.Empty(t, cte.GetChangedTargets())
	assert.Empty(t, cte.GetAddedTargets())

	require.Len(t, cte.GetRemovedTargets(), 1)
	removedID := cte.GetRemovedTargets()[0].GetId()
	assert.Equal(t, "//app:gone", meta.GetTargetIdMapping()[removedID])

	assert.Empty(t, cte.GetNewEdges())
	assert.Empty(t, cte.GetRemovedEdges())
}

func TestCompareTargetGraphsAndEdges_NewEdge(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// First graph: A and B, no edge between them
	first := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 1, Hash: "h1"},
						{Id: 2, Hash: "h2"},
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{1: "//app:A", 2: "//app:B"},
			}},
		},
	}
	// Second graph: A and B, A now depends on B (new edge), hashes changed
	second := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 10, Hash: "h1-new", DirectDependencies: []int32{20}}, // A -> B
						{Id: 20, Hash: "h2"},
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{10: "//app:A", 20: "//app:B"},
			}},
		},
	}

	res, err := c.compareTargetGraphsAndEdges(zap.NewNop(), first, second, -1, false)
	require.NoError(t, err)
	cte, meta := collectCTEResponses(res)

	require.Len(t, cte.GetNewEdges(), 1, "should detect 1 new edge A->B")
	edge := cte.GetNewEdges()[0]
	assert.Equal(t, "//app:A", meta.GetTargetIdMapping()[edge.GetSourceId()])
	assert.Equal(t, "//app:B", meta.GetTargetIdMapping()[edge.GetTargetId()])
	assert.Empty(t, cte.GetRemovedEdges())
}

func TestCompareTargetGraphsAndEdges_RemovedEdge(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// First graph: A depends on B
	first := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 1, Hash: "h1", DirectDependencies: []int32{2}},
						{Id: 2, Hash: "h2"},
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{1: "//app:A", 2: "//app:B"},
			}},
		},
	}
	// Second graph: A no longer depends on B, hash changed
	second := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 10, Hash: "h1-new"}, // A, no deps
						{Id: 20, Hash: "h2"},     // B unchanged
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{10: "//app:A", 20: "//app:B"},
			}},
		},
	}

	res, err := c.compareTargetGraphsAndEdges(zap.NewNop(), first, second, -1, false)
	require.NoError(t, err)
	cte, meta := collectCTEResponses(res)

	require.Len(t, cte.GetRemovedEdges(), 1, "should detect 1 removed edge A->B")
	edge := cte.GetRemovedEdges()[0]
	assert.Equal(t, "//app:A", meta.GetTargetIdMapping()[edge.GetSourceId()])
	assert.Equal(t, "//app:B", meta.GetTargetIdMapping()[edge.GetTargetId()])
	assert.Empty(t, cte.GetNewEdges())
}

func TestCompareTargetGraphsAndEdges_ChangedTargetClassification(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// First: source file A (rule "source file"), lib L depends on A
	first := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 1, Hash: "h1", RuleType: 100},
						{Id: 2, Hash: "h1", RuleType: 200, DirectDependencies: []int32{1}},
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{1: "//app:A", 2: "//app:L"},
				RuleTypeMapping: map[int32]string{100: "source file", 200: "go_library"},
			}},
		},
	}
	// Second: both hashes changed
	second := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 11, Hash: "h2", RuleType: 101},
						{Id: 22, Hash: "h2", RuleType: 201, DirectDependencies: []int32{11}},
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{11: "//app:A", 22: "//app:L"},
				RuleTypeMapping: map[int32]string{101: "source file", 201: "go_library"},
			}},
		},
	}

	res, err := c.compareTargetGraphsAndEdges(zap.NewNop(), first, second, -1, false)
	require.NoError(t, err)
	cte, meta := collectCTEResponses(res)

	require.Len(t, cte.GetChangedTargets(), 2)
	byName := make(map[string]*pb.ChangedTarget)
	for _, ct := range cte.GetChangedTargets() {
		name := meta.GetTargetIdMapping()[ct.GetNewTarget().GetId()]
		byName[name] = ct
	}
	require.Equal(t, pb.CHANGE_TYPE_DIRECT, byName["//app:A"].GetChangeType())
	require.Equal(t, pb.CHANGE_TYPE_DIRECT, byName["//app:L"].GetChangeType())
}

func TestCompareTargetGraphsAndEdges_UnchangedTargetNotReturned(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Both graphs have target A with the same hash — it should not appear in changed_targets.
	first := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{{Id: 1, Hash: "same-hash"}},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{1: "//app:A"},
			}},
		},
	}
	second := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{{Id: 10, Hash: "same-hash"}},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{10: "//app:A"},
			}},
		},
	}

	res, err := c.compareTargetGraphsAndEdges(zap.NewNop(), first, second, -1, false)
	require.NoError(t, err)
	cte, _ := collectCTEResponses(res)
	assert.Empty(t, cte.GetChangedTargets())
	assert.Empty(t, cte.GetAddedTargets())
	assert.Empty(t, cte.GetRemovedTargets())
	assert.Empty(t, cte.GetNewEdges())
	assert.Empty(t, cte.GetRemovedEdges())
}

func TestCompareTargetGraphsAndEdges_EdgePreservedWhenTargetUnchanged(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// A->B edge exists in both revisions (neither target changes).
	mkGraph := func(aID, bID int32, aHash, bHash string) []*pb.GetTargetGraphResponse {
		return []*pb.GetTargetGraphResponse{
			{
				Item: &pb.GetTargetGraphResponse_Targets{
					Targets: &pb.OptimizedTargets{
						Targets: []*pb.OptimizedTarget{
							{Id: aID, Hash: aHash, DirectDependencies: []int32{bID}},
							{Id: bID, Hash: bHash},
						},
					},
				},
			},
			{
				Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{
					TargetIdMapping: map[int32]string{aID: "//app:A", bID: "//app:B"},
				}},
			},
		}
	}

	res, err := c.compareTargetGraphsAndEdges(zap.NewNop(),
		mkGraph(1, 2, "hA", "hB"),
		mkGraph(10, 20, "hA", "hB"),
		-1, false,
	)
	require.NoError(t, err)
	cte, _ := collectCTEResponses(res)
	assert.Empty(t, cte.GetChangedTargets())
	assert.Empty(t, cte.GetNewEdges(), "edge present in both revisions should not be new")
	assert.Empty(t, cte.GetRemovedEdges(), "edge present in both revisions should not be removed")
}

func TestCompareTargetGraphsAndEdges_CanonicalIDs(t *testing.T) {
	c := newTestController(zaptest.NewLogger(t))

	// Two targets in first; one removed, one added, one new edge.
	first := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 1, Hash: "h1"},
						{Id: 2, Hash: "h2"},
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{1: "//app:A", 2: "//app:B"},
			}},
		},
	}
	// Second: B gone, C added, A->C new edge, A hash changed
	second := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 10, Hash: "h1-new", DirectDependencies: []int32{30}}, // A
						{Id: 30, Hash: "h3"}, // C (new)
					},
				},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{10: "//app:A", 30: "//app:C"},
			}},
		},
	}

	res, err := c.compareTargetGraphsAndEdges(zap.NewNop(), first, second, -1, false)
	require.NoError(t, err)

	cte, meta := collectCTEResponses(res)

	// A changed
	var foundA bool
	for _, ct := range cte.GetChangedTargets() {
		if meta.GetTargetIdMapping()[ct.GetNewTarget().GetId()] == "//app:A" {
			foundA = true
		}
	}
	assert.True(t, foundA, "A should appear in changed_targets")

	// C added
	require.Len(t, cte.GetAddedTargets(), 1)
	assert.Equal(t, "//app:C", meta.GetTargetIdMapping()[cte.GetAddedTargets()[0].GetId()])

	// B removed
	require.Len(t, cte.GetRemovedTargets(), 1)
	assert.Equal(t, "//app:B", meta.GetTargetIdMapping()[cte.GetRemovedTargets()[0].GetId()])

	// A->C is a new edge
	require.Len(t, cte.GetNewEdges(), 1)
	newEdge := cte.GetNewEdges()[0]
	assert.Equal(t, "//app:A", meta.GetTargetIdMapping()[newEdge.GetSourceId()])
	assert.Equal(t, "//app:C", meta.GetTargetIdMapping()[newEdge.GetTargetId()])
}

// --- buildEdgeSet ---

func TestBuildEdgeSet_NilMetadata(t *testing.T) {
	byName := map[string]*pb.OptimizedTarget{
		"A": {Id: 1, DirectDependencies: []int32{2}},
	}
	edges := buildEdgeSet(byName, nil)
	assert.Nil(t, edges)
}

func TestBuildEdgeSet_Empty(t *testing.T) {
	edges := buildEdgeSet(map[string]*pb.OptimizedTarget{}, &pb.Metadata{
		TargetIdMapping: map[int32]string{},
	})
	assert.Empty(t, edges)
}

func TestBuildEdgeSet_SingleEdge(t *testing.T) {
	byName := map[string]*pb.OptimizedTarget{
		"//app:A": {Id: 1, DirectDependencies: []int32{2}},
		"//app:B": {Id: 2},
	}
	meta := &pb.Metadata{
		TargetIdMapping: map[int32]string{1: "//app:A", 2: "//app:B"},
	}
	edges := buildEdgeSet(byName, meta)
	require.Contains(t, edges, edgeKey{source: "//app:A", dep: "//app:B"})
	assert.Len(t, edges, 1)
}

func TestBuildEdgeSet_UnknownDepSkipped(t *testing.T) {
	// Dep ID 99 has no entry in the metadata mapping — should be silently skipped.
	byName := map[string]*pb.OptimizedTarget{
		"//app:A": {Id: 1, DirectDependencies: []int32{99}},
	}
	meta := &pb.Metadata{
		TargetIdMapping: map[int32]string{1: "//app:A"},
	}
	edges := buildEdgeSet(byName, meta)
	assert.Empty(t, edges)
}

// --- Full integration test ---

func TestGetChangedTargetsAndEdges_EndToEnd(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsAndEdgesYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())

	var sentResponses []*pb.GetChangedTargetsAndEdgesResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(resp *pb.GetChangedTargetsAndEdgesResponse, _ ...any) error {
		sentResponses = append(sentResponses, resp)
		return nil
	}).AnyTimes()

	store := storagemock.NewMockStorage(ctrl)

	// Build first revision graph: A (hash h1), B (hash h2) with A->B edge.
	var buf1 bytes.Buffer
	w1 := gogio.NewDelimitedWriter(&buf1)
	w1.WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Targets{
			Targets: &pb.OptimizedTargets{
				Targets: []*pb.OptimizedTarget{
					{Id: 1, Hash: "h1", DirectDependencies: []int32{2}},
					{Id: 2, Hash: "h2"},
				},
			},
		},
	})
	w1.WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Metadata{
			Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{1: "//app:A", 2: "//app:B"},
			},
		},
	})
	graph1Bytes := buf1.Bytes()

	// Build second revision graph:
	// - A hash changed (h1-new), A->B edge removed, A->C edge added.
	// - B unchanged.
	// - C new target.
	var buf2 bytes.Buffer
	w2 := gogio.NewDelimitedWriter(&buf2)
	w2.WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Targets{
			Targets: &pb.OptimizedTargets{
				Targets: []*pb.OptimizedTarget{
					{Id: 10, Hash: "h1-new", DirectDependencies: []int32{30}}, // A, now depends on C
					{Id: 20, Hash: "h2"}, // B unchanged
					{Id: 30, Hash: "h3"}, // C new
				},
			},
		},
	})
	w2.WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Metadata{
			Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{10: "//app:A", 20: "//app:B", 30: "//app:C"},
			},
		},
	})
	graph2Bytes := buf2.Bytes()

	putDone := make(chan struct{}, 1)
	store.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req storage.DownloadRequest) (*storage.DownloadResponse, error) {
			switch {
			case strings.Contains(req.Key, "compared-targets-and-edges"):
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
		}).AnyTimes()
	store.EXPECT().Put(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ storage.UploadRequest) error {
			putDone <- struct{}{}
			return nil
		})

	c := NewController(Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      store,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	err := c.GetChangedTargetsAndEdges(&pb.GetChangedTargetsAndEdgesRequest{
		FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
	}, stream)
	require.NoError(t, err)

	select {
	case <-putDone:
	case <-time.After(time.Second):
		assert.Fail(t, "timed out waiting for cache write")
	}

	require.NotEmpty(t, sentResponses)
	cte, meta := collectCTEResponses(sentResponses)
	require.NotNil(t, cte)
	require.NotNil(t, meta)

	// A should be changed (hash changed + dep changed -> DIRECT)
	var foundA bool
	for _, ct := range cte.GetChangedTargets() {
		if meta.GetTargetIdMapping()[ct.GetNewTarget().GetId()] == "//app:A" {
			foundA = true
			assert.Equal(t, pb.CHANGE_TYPE_DIRECT, ct.GetChangeType())
		}
	}
	assert.True(t, foundA, "A must appear in changed_targets")

	// C added
	require.Len(t, cte.GetAddedTargets(), 1)
	assert.Equal(t, "//app:C", meta.GetTargetIdMapping()[cte.GetAddedTargets()[0].GetId()])

	// No removed targets (B is present in both)
	assert.Empty(t, cte.GetRemovedTargets())

	// A->C is a new edge; A->B is removed
	newEdgeNames := edgesByName(cte.GetNewEdges(), meta)
	removedEdgeNames := edgesByName(cte.GetRemovedEdges(), meta)
	assert.Contains(t, newEdgeNames, [2]string{"//app:A", "//app:C"})
	assert.Contains(t, removedEdgeNames, [2]string{"//app:A", "//app:B"})
}

// edgesByName converts a slice of Edge proto messages to [2]string{source, dep} pairs using metadata.
func edgesByName(edges []*pb.Edge, meta *pb.Metadata) [][2]string {
	var out [][2]string
	for _, e := range edges {
		out = append(out, [2]string{
			meta.GetTargetIdMapping()[e.GetSourceId()],
			meta.GetTargetIdMapping()[e.GetTargetId()],
		})
	}
	return out
}

// --- Cache tests ---

func TestGetChangedTargetsAndEdges_CacheHit(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsAndEdgesYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())

	// Build a cached response with one ChangedTargetsAndEdges message and one Metadata message.
	cachedCTE := &pb.GetChangedTargetsAndEdgesResponse{
		Item: &pb.GetChangedTargetsAndEdgesResponse_ChangedTargetsAndEdges{
			ChangedTargetsAndEdges: &pb.ChangedTargetsAndEdges{},
		},
	}
	cachedMeta := &pb.GetChangedTargetsAndEdgesResponse{
		Item: &pb.GetChangedTargetsAndEdgesResponse_Metadata{
			Metadata: &pb.Metadata{},
		},
	}
	var buf bytes.Buffer
	w := gogio.NewDelimitedWriter(&buf)
	w.WriteMsg(cachedCTE)
	w.WriteMsg(cachedMeta)
	cachedBytes := buf.Bytes()

	store := storagemock.NewMockStorage(ctrl)
	// First two Gets resolve treehashes; third fetches the cached comparison result.
	gomock.InOrder(
		store.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(&storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash1")))}, nil),
		store.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(&storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("treehash2")))}, nil),
		store.EXPECT().Get(gomock.Any(), gomock.Any()).
			Return(&storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(cachedBytes))}, nil),
	)

	stream.EXPECT().Send(gomock.Any()).Return(nil).AnyTimes()

	c := NewController(Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      store,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	err := c.GetChangedTargetsAndEdges(&pb.GetChangedTargetsAndEdgesRequest{
		FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
	}, stream)
	require.NoError(t, err)
}

func TestGetChangedTargetsAndEdges_CacheCorrupt(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsAndEdgesYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())

	// Build two valid per-revision graphs (empty metadata) for the fallthrough compute.
	var emptyGraph bytes.Buffer
	gogio.NewDelimitedWriter(&emptyGraph).WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Metadata{Metadata: &pb.Metadata{}},
	})
	emptyGraphBytes := emptyGraph.Bytes()

	// Corrupt cache blob: a valid varint length followed by truncated body.
	// This causes gogio.ReadMsg to return a non-EOF error mid-read.
	corruptBytes := []byte{0x80, 0x80, 0x80, 0x80, 0x80} // oversized varint → read error

	store := storagemock.NewMockStorage(ctrl)
	store.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req storage.DownloadRequest) (*storage.DownloadResponse, error) {
			switch {
			case strings.Contains(req.Key, "compared-targets-and-edges"):
				// Return corrupt data so the cache falls through to compute.
				return &storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(corruptBytes))}, nil
			case strings.Contains(req.Key, "sha1") || strings.Contains(req.Key, "sha2"):
				return &storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("th")))}, nil
			default:
				return &storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(emptyGraphBytes))}, nil
			}
		}).AnyTimes()

	// After recompute, cache write is attempted in a goroutine.
	putDone := make(chan struct{}, 1)
	store.EXPECT().Put(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ storage.UploadRequest) error {
		putDone <- struct{}{}
		return nil
	})

	stream.EXPECT().Send(gomock.Any()).Return(nil).AnyTimes()

	c := NewController(Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      store,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	err := c.GetChangedTargetsAndEdges(&pb.GetChangedTargetsAndEdgesRequest{
		FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
	}, stream)
	require.NoError(t, err)

	select {
	case <-putDone:
	case <-time.After(time.Second):
		assert.Fail(t, "cache write goroutine did not complete in time")
	}
}

func TestGetChangedTargetsAndEdges_CacheWriteAfterCompute(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsAndEdgesYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())

	var sentResponses []*pb.GetChangedTargetsAndEdgesResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(resp *pb.GetChangedTargetsAndEdgesResponse, _ ...any) error {
		sentResponses = append(sentResponses, resp)
		return nil
	}).AnyTimes()

	// Build first revision graph: A and B with A->B edge.
	var buf1 bytes.Buffer
	w1 := gogio.NewDelimitedWriter(&buf1)
	w1.WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Targets{
			Targets: &pb.OptimizedTargets{
				Targets: []*pb.OptimizedTarget{
					{Id: 1, Hash: "h1", DirectDependencies: []int32{2}},
					{Id: 2, Hash: "h2"},
				},
			},
		},
	})
	w1.WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Metadata{
			Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{1: "//app:A", 2: "//app:B"},
			},
		},
	})
	graph1Bytes := buf1.Bytes()

	// Build second revision graph: A hash changed, B->C edge added.
	var buf2 bytes.Buffer
	w2 := gogio.NewDelimitedWriter(&buf2)
	w2.WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Targets{
			Targets: &pb.OptimizedTargets{
				Targets: []*pb.OptimizedTarget{
					{Id: 10, Hash: "h1-new", DirectDependencies: []int32{20}},
					{Id: 20, Hash: "h2"},
				},
			},
		},
	})
	w2.WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Metadata{
			Metadata: &pb.Metadata{
				TargetIdMapping: map[int32]string{10: "//app:A", 20: "//app:B"},
			},
		},
	})
	graph2Bytes := buf2.Bytes()

	store := storagemock.NewMockStorage(ctrl)
	store.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req storage.DownloadRequest) (*storage.DownloadResponse, error) {
			switch {
			case strings.Contains(req.Key, "compared-targets-and-edges"):
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
		}).AnyTimes()

	putDone := make(chan struct{}, 1)
	store.EXPECT().Put(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ storage.UploadRequest) error {
		putDone <- struct{}{}
		return nil
	})

	c := NewController(Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      store,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	})

	err := c.GetChangedTargetsAndEdges(&pb.GetChangedTargetsAndEdgesRequest{
		FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
	}, stream)
	require.NoError(t, err)

	select {
	case <-putDone:
	case <-time.After(time.Second):
		assert.Fail(t, "cache write goroutine did not complete in time")
	}

	require.NotEmpty(t, sentResponses)
	cte, meta := collectCTEResponses(sentResponses)
	require.NotNil(t, cte)
	require.NotNil(t, meta)

	// A's hash changed but dep names are the same (still //app:B) and no attrs → INDIRECT
	var foundA bool
	for _, ct := range cte.GetChangedTargets() {
		if meta.GetTargetIdMapping()[ct.GetNewTarget().GetId()] == "//app:A" {
			foundA = true
			assert.Equal(t, pb.CHANGE_TYPE_INDIRECT, ct.GetChangeType())
		}
	}
	assert.True(t, foundA, "A must appear in changed_targets")
}

// --- sendWithDistanceFilterForEdges ---

func TestSendWithDistanceFilterForEdges_NonChangedTargetFieldsPreserved(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsAndEdgesYARPCServer(ctrl)

	// changed_targets contain a mix that will be filtered; added/removed/edges
	// and metadata must pass through verbatim regardless of distance bound.
	addedTargets := []*pb.OptimizedTarget{{Id: 100}, {Id: 101}}
	removedTargets := []*pb.OptimizedTarget{{Id: 200}}
	newEdges := []*pb.Edge{{SourceId: 1, TargetId: 2}, {SourceId: 3, TargetId: 4}}
	removedEdges := []*pb.Edge{{SourceId: 5, TargetId: 6}}
	meta := &pb.Metadata{TargetIdMapping: map[int32]string{1: "//app:T"}}

	responses := []*pb.GetChangedTargetsAndEdgesResponse{
		{
			Item: &pb.GetChangedTargetsAndEdgesResponse_ChangedTargetsAndEdges{
				ChangedTargetsAndEdges: &pb.ChangedTargetsAndEdges{
					ChangedTargets: []*pb.ChangedTarget{
						{Distance: 0, ChangeType: pb.CHANGE_TYPE_DIRECT},
						{Distance: 99, ChangeType: pb.CHANGE_TYPE_INDIRECT}, // filtered
						{Distance: -1, ChangeType: pb.CHANGE_TYPE_NEW},      // filtered
					},
					AddedTargets:   addedTargets,
					RemovedTargets: removedTargets,
					NewEdges:       newEdges,
					RemovedEdges:   removedEdges,
				},
			},
		},
		{
			Item: &pb.GetChangedTargetsAndEdgesResponse_Metadata{Metadata: meta},
		},
	}

	var sent []*pb.GetChangedTargetsAndEdgesResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(r *pb.GetChangedTargetsAndEdgesResponse, _ ...any) error {
		sent = append(sent, r)
		return nil
	}).Times(2)

	require.NoError(t, sendWithDistanceFilterForEdges(stream, responses, 1))

	cte := sent[0].GetChangedTargetsAndEdges()
	require.Len(t, cte.GetChangedTargets(), 1, "only distance-0 target should survive")
	assert.Equal(t, int32(0), cte.GetChangedTargets()[0].GetDistance())

	// All non-changed-target fields preserved verbatim.
	assert.Equal(t, addedTargets, cte.GetAddedTargets())
	assert.Equal(t, removedTargets, cte.GetRemovedTargets())
	assert.Equal(t, newEdges, cte.GetNewEdges())
	assert.Equal(t, removedEdges, cte.GetRemovedEdges())
	assert.Equal(t, meta, sent[1].GetMetadata())
}

func TestSendWithDistanceFilterForEdges_SendError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsAndEdgesYARPCServer(ctrl)

	responses := []*pb.GetChangedTargetsAndEdgesResponse{
		{
			Item: &pb.GetChangedTargetsAndEdgesResponse_ChangedTargetsAndEdges{
				ChangedTargetsAndEdges: &pb.ChangedTargetsAndEdges{},
			},
		},
	}

	stream.EXPECT().Send(gomock.Any()).Return(errors.New("send error"))

	err := sendWithDistanceFilterForEdges(stream, responses, -1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "send error")
}
