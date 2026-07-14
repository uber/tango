package mapper

import (
	"context"
	stderrs "errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/tangopb"
	"go.uber.org/yarpc/encoding/protobuf"
	"go.uber.org/yarpc/yarpcerrors"
)

func TestToProtoError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantCode  tangopb.ErrorCode
		wantYARPC yarpcerrors.Code
	}{
		{
			name:      "context canceled",
			err:       context.Canceled,
			wantCode:  tangopb.ERROR_CANCELLED,
			wantYARPC: yarpcerrors.CodeCancelled,
		},
		{
			name:      "classified error",
			err:       tangoerrors.NewUser(tangoerrors.FailureSourceConfig, stderrs.New("bad input")),
			wantCode:  tangopb.ERROR_USER,
			wantYARPC: yarpcerrors.CodeInvalidArgument,
		},
		{
			name:      "unclassified error",
			err:       stderrs.New("plain error"),
			wantCode:  tangopb.ERROR_UNKNOWN,
			wantYARPC: yarpcerrors.CodeUnknown,
		},
		{
			name:      "wrapped classified error",
			err:       stderrs.Join(tangoerrors.NewUser(tangoerrors.FailureSourceConfig, stderrs.New("bad input"))),
			wantCode:  tangopb.ERROR_USER,
			wantYARPC: yarpcerrors.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ToProtoError(tt.err)
			require.Error(t, err)

			assert.Equal(t, tt.wantYARPC, yarpcerrors.FromError(err).Code())

			details := protobuf.GetErrorDetails(err)
			require.Len(t, details, 1)
			tangoErr, ok := details[0].(*tangopb.TangoError)
			require.True(t, ok)
			assert.Equal(t, tt.wantCode, tangoErr.Code)
			assert.Equal(t, tt.err.Error(), tangoErr.Message)
		})
	}
}

func TestToProtoErrorCode(t *testing.T) {
	tests := []struct {
		name string
		code tangoerrors.ErrorCode
		want tangopb.ErrorCode
	}{
		{name: "cancelled", code: tangoerrors.ErrorCancelled, want: tangopb.ERROR_CANCELLED},
		{name: "user", code: tangoerrors.ErrorUser, want: tangopb.ERROR_USER},
		{name: "infra", code: tangoerrors.ErrorInfra, want: tangopb.ERROR_INFRA},
		{name: "infra retryable", code: tangoerrors.ErrorInfraRetryable, want: tangopb.ERROR_INFRA_RETRYABLE},
		{name: "unknown", code: tangoerrors.ErrorUnknown, want: tangopb.ERROR_UNKNOWN},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, toProtoErrorCode(tt.code))
		})
	}
}

func TestToYARPCCode(t *testing.T) {
	tests := []struct {
		name string
		code tangoerrors.ErrorCode
		want yarpcerrors.Code
	}{
		{name: "cancelled", code: tangoerrors.ErrorCancelled, want: yarpcerrors.CodeCancelled},
		{name: "user", code: tangoerrors.ErrorUser, want: yarpcerrors.CodeInvalidArgument},
		{name: "infra", code: tangoerrors.ErrorInfra, want: yarpcerrors.CodeInternal},
		{name: "infra retryable", code: tangoerrors.ErrorInfraRetryable, want: yarpcerrors.CodeInternal},
		{name: "unknown", code: tangoerrors.ErrorUnknown, want: yarpcerrors.CodeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, toYARPCCode(tt.code))
		})
	}
}
