package errors

import (
	"context"
	stderrs "errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

func TestNewError_Constructors(t *testing.T) {
	underlying := stderrs.New("boom")

	tests := []struct {
		name     string
		newErr   func(error) error
		wantCode ErrorCode
	}{
		{
			name:     "infra",
			newErr:   NewInfra,
			wantCode: ErrorInfra,
		},
		{
			name:     "user",
			newErr:   NewUser,
			wantCode: ErrorUser,
		},
		{
			name:     "infra retryable",
			newErr:   NewInfraRetryable,
			wantCode: ErrorInfraRetryable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.newErr(underlying)

			var te *TangoError
			require.True(t, stderrs.As(err, &te))
			assert.Equal(t, tt.wantCode, te.errorCode)
			assert.Equal(t, underlying.Error(), te.Error())
		})
	}
}

func TestNewError_RewrappingOverwritesCode(t *testing.T) {
	inner := NewUser(stderrs.New("original"))

	outer := NewInfra(inner)

	var te *TangoError
	require.True(t, stderrs.As(outer, &te))
	assert.Equal(t, ErrorInfra, te.errorCode)
}

func TestGetErrorCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ErrorCode
	}{
		{
			name: "raw context.Canceled",
			err:  context.Canceled,
			want: ErrorCancelled,
		},
		{
			name: "wrapped context.Canceled",
			err:  fmt.Errorf("read: %w", context.Canceled),
			want: ErrorCancelled,
		},
		{
			name: "TangoError without cancellation",
			err:  NewUser(stderrs.New("bad input")),
			want: ErrorUser,
		},
		{
			name: "unclassified error",
			err:  stderrs.New("boom"),
			want: ErrorInfra,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, GetErrorCode(tt.err))
		})
	}
}

func TestFields(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{
			name:     "classified error",
			err:      NewUser(stderrs.New("boom")),
			wantCode: "user",
		},
		{
			name:     "unclassified error",
			err:      stderrs.New("boom"),
			wantCode: "infra",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc := zapcore.NewMapObjectEncoder()
			for _, field := range Fields(tt.err) {
				field.AddTo(enc)
			}

			assert.Equal(t, tt.err.Error(), enc.Fields["error"])
			assert.Equal(t, tt.wantCode, enc.Fields["error_code"])
		})
	}
}
