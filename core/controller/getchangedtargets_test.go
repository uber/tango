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
	orchestratormock "github.com/uber/tango/orchestrator/orchestratormock"
	pb "github.com/uber/tango/tangopb"
	tangomock "github.com/uber/tango/tangopb/tangopbmock"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"google.golang.org/protobuf/encoding/protodelim"
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
	c := &controller{logger: zap.NewNop()}

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

	response, err := c.compareTargetGraphs(context.Background(), []*pb.GetTargetGraphResponse{firstGraph}, []*pb.GetTargetGraphResponse{secondGraph}, nil)
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

func TestGetChangedTargets_GetGraphError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)

	storagemock := storagemock.NewMockStorage(ctrl)
	storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, errors.New("graph error")).Times(2)

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

	stream.EXPECT().Send(gomock.Any()).Return(errors.New("send error"))
	storagemock := storagemock.NewMockStorage(ctrl)
	storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&storage.DownloadResponse{ReadCloser: nil}, nil).AnyTimes()

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
}

func TestGetChangedTargets_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)

	stream.EXPECT().Send(gomock.Any()).Return(nil).Times(2)
	storagemock := storagemock.NewMockStorage(ctrl)
	// Prepare graph bytes to be returned for graph fetches
	graph := pb.GetTargetGraphResponse{Item: &pb.GetTargetGraphResponse_Targets{Targets: &pb.OptimizedTargets{}}}
	var buf bytes.Buffer
	_, err := protodelim.MarshalTo(&buf, &graph)
	require.NoError(t, err)
	// Controller.getGraph performs two storage lookups per revision:
	// 1) treehash cache -> returns bytes (content not important)
	// 2) graph by treehash -> returns marshaled graph
	// We set four Get calls total; concurrency means order may vary, but returning either is acceptable.
	storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("th")))}, nil).Times(2)
	storagemock.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(buf.Bytes()))}, nil).Times(2)
	orchestrator := orchestratormock.NewMockOrchestrator(ctrl)
	c := NewController(Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      storagemock,
		Orchestrator: orchestrator,
	})

	request := &pb.GetChangedTargetsRequest{
		FirstRevision:  &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha1"},
		SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
	}

	err = c.GetChangedTargets(request, stream)
	assert.NoError(t, err)
}

func TestCompareTargetGraphs_NewTarget_CanonicalIDs(t *testing.T) {
	c := &controller{logger: zaptest.NewLogger(t)}

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
	res, err := c.compareTargetGraphs(context.Background(), first, second, nil)
	require.NoError(t, err)
	require.Len(t, res, 2)
	cs := res[0].GetChangedTargets()
	require.NotNil(t, cs)
	require.Len(t, cs.GetChangedTargets(), 1)
	ct := cs.GetChangedTargets()[0]
	require.Equal(t, pb.ChangeType_CHANGE_TYPE_NEW, ct.GetChangeType())
	// ID used in target should match canonical metadata mapping
	meta := res[1].GetMetadata()
	require.NotNil(t, meta)
	newID := ct.GetNewTarget().GetId()
	require.Equal(t, "//app:new", meta.GetTargetIdMapping()[newID])
}

func TestCompareTargetGraphs_SourceFileDirectAndPropagation(t *testing.T) {
	c := &controller{logger: zaptest.NewLogger(t)}

	// Old: source file A (id 1, hash h1), lib L (id 2, hash h1, dep -> A)
	first := []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{
					Targets: []*pb.OptimizedTarget{
						{Id: 1, Hash: "h1", RuleType: 100},                      // "source file"
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
						{Id: 11, Hash: "h2", RuleType: 101},                      // "source file"
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
	res, err := c.compareTargetGraphs(context.Background(), first, second, nil)
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
	require.Equal(t, pb.ChangeType_CHANGE_TYPE_DIRECT, aCT.GetChangeType())
	require.Equal(t, pb.ChangeType_CHANGE_TYPE_DIRECT, lCT.GetChangeType())
	// Old and new IDs must match for each changed target under canonical metadata
	require.Equal(t, aCT.GetOldTarget().GetId(), aCT.GetNewTarget().GetId())
	require.Equal(t, lCT.GetOldTarget().GetId(), lCT.GetNewTarget().GetId())
}

func TestCompareTargetGraphs_IndirectWhenNoSourceDep(t *testing.T) {
	c := &controller{logger: zaptest.NewLogger(t)}

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
	res, err := c.compareTargetGraphs(context.Background(), first, second, nil)
	require.NoError(t, err)
	cs := res[0].GetChangedTargets()
	require.NotNil(t, cs)
	require.Len(t, cs.GetChangedTargets(), 1)
	require.Equal(t, pb.ChangeType_CHANGE_TYPE_INDIRECT, cs.GetChangedTargets()[0].GetChangeType())
}

func TestCompareTargetGraphs_DirectWhenDependenciesChanged(t *testing.T) {
	c := &controller{logger: zaptest.NewLogger(t)}

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
	res, err := c.compareTargetGraphs(context.Background(), first, second, nil)
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
	require.Equal(t, pb.ChangeType_CHANGE_TYPE_DIRECT, targetT.GetChangeType(), "Target with changed dependencies should be marked as DIRECT")
}

func TestCompareTargetGraphs_DirectWhenAttributesChanged(t *testing.T) {
	c := &controller{logger: zaptest.NewLogger(t)}

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
	res, err := c.compareTargetGraphs(context.Background(), first, second, nil)
	require.NoError(t, err)
	cs := res[0].GetChangedTargets()
	require.NotNil(t, cs)
	require.Len(t, cs.GetChangedTargets(), 1)
	require.Equal(t, pb.ChangeType_CHANGE_TYPE_DIRECT, cs.GetChangedTargets()[0].GetChangeType(), "Target with changed attributes should be marked as DIRECT")
}

func TestCompareTargetGraphs_DirectWhenNewAttributeAdded(t *testing.T) {
	c := &controller{logger: zaptest.NewLogger(t)}

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
	res, err := c.compareTargetGraphs(context.Background(), first, second, nil)
	require.NoError(t, err)
	cs := res[0].GetChangedTargets()
	require.NotNil(t, cs)
	require.Len(t, cs.GetChangedTargets(), 1)
	require.Equal(t, pb.ChangeType_CHANGE_TYPE_DIRECT, cs.GetChangedTargets()[0].GetChangeType(), "Target with new attribute added should be marked as DIRECT")
}

func TestCompareTargetGraphs_IndirectWhenOnlyHashChanged(t *testing.T) {
	c := &controller{logger: zaptest.NewLogger(t)}

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
							DirectDependencies: []int32{20}, // Same dep (//app:A)
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
	res, err := c.compareTargetGraphs(context.Background(), first, second, nil)
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
	require.Equal(t, pb.ChangeType_CHANGE_TYPE_INDIRECT, targetT.GetChangeType(), "Target with only hash change (not deps/attrs) should be marked as INDIRECT")
}
