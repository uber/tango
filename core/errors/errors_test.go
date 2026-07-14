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
		newErr   func(FailureSource, error) error
		source   FailureSource
		wantCode ErrorCode
	}{
		{
			name:     "infra",
			newErr:   NewInfra,
			source:   FailureSourceGit,
			wantCode: ErrorInfra,
		},
		{
			name:     "user",
			newErr:   NewUser,
			source:   FailureSourceConfig,
			wantCode: ErrorUser,
		},
		{
			name:     "infra retryable",
			newErr:   NewInfraRetryable,
			source:   FailureSourceStorage,
			wantCode: ErrorInfraRetryable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.newErr(tt.source, underlying)

			var te *TangoError
			require.True(t, stderrs.As(err, &te))
			assert.Equal(t, tt.source, te.failureSource)
			assert.Equal(t, tt.wantCode, te.errorCode)
			assert.Equal(t, underlying.Error(), te.Error())
		})
	}
}

func TestNewError_NilSourceDefaultsToUnknown(t *testing.T) {
	err := NewInfra(nil, stderrs.New("boom"))

	var te *TangoError
	require.True(t, stderrs.As(err, &te))
	assert.Equal(t, FailureSourceUnknown, te.failureSource)
}

func TestNewError_RewrappingPreservesOriginalClassification(t *testing.T) {
	inner := NewUser(FailureSourceConfig, stderrs.New("original"))

	outer := NewInfra(FailureSourceGit, inner)

	var te *TangoError
	require.True(t, stderrs.As(outer, &te))
	assert.Equal(t, FailureSourceConfig, te.failureSource)
	assert.Equal(t, ErrorUser, te.errorCode)
}

func TestFailureSource_String(t *testing.T) {
	assert.Equal(t, "fake-source", source("fake-source").String())
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
			name: "context.Canceled wrapped in a TangoError",
			err:  NewInfra(FailureSourceStorage, context.Canceled),
			want: ErrorCancelled,
		},
		{
			name: "TangoError without cancellation",
			err:  NewUser(FailureSourceConfig, stderrs.New("bad input")),
			want: ErrorUser,
		},
		{
			name: "unclassified error",
			err:  stderrs.New("boom"),
			want: ErrorUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, GetErrorCode(tt.err))
		})
	}
}

func TestGetFailureSource(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want FailureSource
	}{
		{
			name: "TangoError source",
			err:  NewInfra(FailureSourceStorage, stderrs.New("boom")),
			want: FailureSourceStorage,
		},
		{
			name: "unclassified error",
			err:  stderrs.New("boom"),
			want: FailureSourceUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, GetFailureSource(tt.err))
		})
	}
}

func TestFields(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCode   string
		wantSource string
	}{
		{
			name:       "classified error",
			err:        NewUser(FailureSourceConfig, stderrs.New("boom")),
			wantCode:   "user",
			wantSource: "config",
		},
		{
			name:       "unclassified error",
			err:        stderrs.New("boom"),
			wantCode:   "unknown",
			wantSource: "unknown",
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
			assert.Equal(t, tt.wantSource, enc.Fields["failure_source"])
		})
	}
}
