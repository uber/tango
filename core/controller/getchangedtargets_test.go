package controller

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/uber/tango/core/storage"
	storagemock "github.com/uber/tango/core/storage/storagemock"
	orchestratormock "github.com/uber/tango/orchestrator/orchestratormock"
	pb "github.com/uber/tango/tangopb"
	tangomock "github.com/uber/tango/tangopb/tangopbmock"
	gogio "github.com/gogo/protobuf/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	ctrl := gomock.NewController(t)
	storagemock := storagemock.NewMockStorage(ctrl)

	c := NewController(Params{
		Logger:       zap.NewNop(),
		Storage:      storagemock,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	}).(*controller)

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

	stream.EXPECT().Send(gomock.Any()).Return(nil)
	storagemock := storagemock.NewMockStorage(ctrl)
	// Prepare graph bytes to be returned for graph fetches
	graph := pb.GetTargetGraphResponse{Item: &pb.GetTargetGraphResponse_Targets{Targets: &pb.OptimizedTargets{}}}
	var buf bytes.Buffer
	err := gogio.NewDelimitedWriter(&buf).WriteMsg(&graph)
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
