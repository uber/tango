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
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/core/storage"
	storagemock "github.com/uber/tango/core/storage/storagemock"
	pb "github.com/uber/tango/tangopb"
	tangomock "github.com/uber/tango/tangopb/tangopbmock"
	"go.uber.org/mock/gomock"
	"go.uber.org/yarpc/encoding/protobuf"
	"go.uber.org/yarpc/yarpcerrors"
	"go.uber.org/zap/zaptest"
)

func TestHandlerErrors_ReturnTangoErrorDetail(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode pb.ErrorCode
	}{
		{
			name:     "user error surfaces ERROR_USER",
			err:      tangoerrors.NewUser(errors.New("bad input")),
			wantCode: pb.ERROR_USER,
		},
		{
			name:     "infra error surfaces ERROR_INFRA",
			err:      tangoerrors.NewInfra(errors.New("storage down")),
			wantCode: pb.ERROR_INFRA,
		},
		{
			name:     "infra retryable error surfaces ERROR_INFRA_RETRYABLE",
			err:      tangoerrors.NewInfraRetryable(errors.New("transient")),
			wantCode: pb.ERROR_INFRA_RETRYABLE,
		},
		{
			name:     "unclassified error defaults to ERROR_INFRA",
			err:      errors.New("unknown"),
			wantCode: pb.ERROR_INFRA,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wireErr := toWireError(tt.err)
			require.Error(t, wireErr)

			// The YARPC code should always be Internal.
			assert.Equal(t, yarpcerrors.CodeInternal, yarpcerrors.FromError(wireErr).Code())

			// Extract the TangoError detail.
			details := protobuf.GetErrorDetails(wireErr)
			require.Len(t, details, 1)
			tangoErr, ok := details[0].(*pb.TangoError)
			require.True(t, ok)
			assert.Equal(t, tt.wantCode, tangoErr.Code)
		})
	}
}

// TestGetTargetGraph_ValidationError_WiresTangoError verifies that a validation
// failure in GetTargetGraph returns a YARPC error carrying a TangoError detail
// with ERROR_USER code. This is the end-to-end contract: the handler wraps
// validation errors with tangoerrors.NewUser, and the defer converts them
// through toWireError before returning to the transport.
func TestGetTargetGraph_ValidationError_WiresTangoError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())

	c := NewController(context.Background(), Params{
		RepoConfig: allowAnyRepositoryConfigProvider{},
		Logger:     zaptest.NewLogger(t),
	})

	// Missing BaseSha triggers a validation error classified as ERROR_USER.
	err := c.GetTargetGraph(&pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{
			Remote: "repo:go-code",
		},
	}, stream)
	require.Error(t, err)

	assert.Equal(t, yarpcerrors.CodeInternal, yarpcerrors.FromError(err).Code())
	details := protobuf.GetErrorDetails(err)
	require.Len(t, details, 1)
	tangoErr, ok := details[0].(*pb.TangoError)
	require.True(t, ok)
	assert.Equal(t, pb.ERROR_USER, tangoErr.Code)
}

// TestGetChangedTargets_ValidationError_WiresTangoError verifies that a
// validation failure in GetChangedTargets returns a YARPC error carrying a
// TangoError detail with ERROR_USER code.
func TestGetChangedTargets_ValidationError_WiresTangoError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background()).AnyTimes()

	c := NewController(context.Background(), Params{
		RepoConfig: allowAnyRepositoryConfigProvider{},
		Logger:     zaptest.NewLogger(t),
	})

	// Missing first revision triggers validation error classified as ERROR_USER.
	err := c.GetChangedTargets(&pb.GetChangedTargetsRequest{
		SecondRevision: &pb.BuildDescription{Remote: "repo:go-code", BaseSha: "sha2"},
	}, stream)
	require.Error(t, err)

	assert.Equal(t, yarpcerrors.CodeInternal, yarpcerrors.FromError(err).Code())
	details := protobuf.GetErrorDetails(err)
	require.Len(t, details, 1)
	tangoErr, ok := details[0].(*pb.TangoError)
	require.True(t, ok)
	assert.Equal(t, pb.ERROR_USER, tangoErr.Code)
}

// TestGetTargetGraph_InfraError_WiresTangoError verifies that an infra-classified
// error (e.g. storage failure) returns a YARPC error with ERROR_INFRA code.
func TestGetTargetGraph_InfraError_WiresTangoError(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetTargetGraphYARPCServer(ctrl)
	stream.EXPECT().Context().Return(context.Background())

	store := storagemock.NewMockStorage(ctrl)
	store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{}, errors.New("disk on fire"))

	c := NewController(context.Background(), Params{
		RepoConfig: allowAnyRepositoryConfigProvider{},
		Logger:     zaptest.NewLogger(t),
		Storage:    store,
	})

	err := c.GetTargetGraph(&pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{
			Remote:   "repo:go-code",
			BaseSha:  "sha",
			Strategy: pb.COMPUTATION_STRATEGY_UNSET,
		},
	}, stream)
	require.Error(t, err)

	assert.Equal(t, yarpcerrors.CodeInternal, yarpcerrors.FromError(err).Code())
	details := protobuf.GetErrorDetails(err)
	require.Len(t, details, 1)
	tangoErr, ok := details[0].(*pb.TangoError)
	require.True(t, ok)
	assert.Equal(t, pb.ERROR_INFRA, tangoErr.Code)
}

func TestToWireError_Nil(t *testing.T) {
	assert.NoError(t, toWireError(nil))
}
