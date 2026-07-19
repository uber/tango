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
		name     string
		err      error
		wantCode tangopb.ErrorCode
	}{
		{
			name:     "context canceled",
			err:      context.Canceled,
			wantCode: tangopb.ERROR_CANCELLED,
		},
		{
			name:     "classified error",
			err:      tangoerrors.NewUser(stderrs.New("bad input")),
			wantCode: tangopb.ERROR_USER,
		},
		{
			name:     "unclassified error",
			err:      stderrs.New("plain error"),
			wantCode: tangopb.ERROR_INFRA,
		},
		{
			name:     "wrapped classified error",
			err:      stderrs.Join(tangoerrors.NewUser(stderrs.New("bad input"))),
			wantCode: tangopb.ERROR_USER,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ToProtoError(tt.err)
			require.Error(t, err)

			assert.Equal(t, yarpcerrors.CodeInternal, yarpcerrors.FromError(err).Code())

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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, toProtoErrorCode(tt.code))
		})
	}
}
