package mapper

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/tangopb"
)

func TestProtoToBuildDescription(t *testing.T) {
	tests := []struct {
		name    string
		desc    *tangopb.BuildDescription
		want    entity.BuildDescription
		wantErr bool
	}{
		{
			name:    "nil",
			desc:    nil,
			wantErr: true,
		},
		{
			name:    "empty",
			desc:    &tangopb.BuildDescription{},
			wantErr: true,
		},
		{
			name:    "missing remote",
			desc:    &tangopb.BuildDescription{BaseSha: "abc123"},
			wantErr: true,
		},
		{
			name:    "missing base_sha",
			desc:    &tangopb.BuildDescription{Remote: "git@example.com:org/repo"},
			wantErr: true,
		},
		{
			name: "full",
			desc: &tangopb.BuildDescription{
				Remote:  "git@example.com:org/repo",
				BaseSha: "abc123",
				Requests: []*tangopb.Request{
					{Url: "github://github.com/org/repo/pull/1/1111111111111111111111111111111111111111"},
					// URL format is orchestrator-specific; the mapper passes
					// it through opaquely without validation.
					{Url: "https://example.com/pr/2"},
				},
				Strategy: tangopb.COMPUTATION_STRATEGY_NATIVE,
			},
			want: entity.BuildDescription{
				Remote:  "git@example.com:org/repo",
				BaseSha: "abc123",
				ChangeRequests: []entity.ChangeRequest{
					{URL: "github://github.com/org/repo/pull/1/1111111111111111111111111111111111111111"},
					{URL: "https://example.com/pr/2"},
				},
				Strategy: entity.ComputationStrategyNative,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ProtoToBuildDescription(tt.desc)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateComputationStrategy(t *testing.T) {
	tests := []struct {
		name     string
		in       tangopb.ComputationStrategy
		want     entity.ComputationStrategy
		wantErr  bool
		wantCode tangoerrors.ErrorCode
	}{
		{
			name: "unset is accepted",
			in:   tangopb.COMPUTATION_STRATEGY_UNSET,
			want: entity.ComputationStrategyUnset,
		},
		{
			name: "shell is accepted",
			in:   tangopb.COMPUTATION_STRATEGY_SHELL,
			want: entity.ComputationStrategyShell,
		},
		{
			name: "native is accepted",
			in:   tangopb.COMPUTATION_STRATEGY_NATIVE,
			want: entity.ComputationStrategyNative,
		},
		{
			name:     "invalid is rejected",
			in:       tangopb.COMPUTATION_STRATEGY_INVALID,
			wantErr:  true,
			wantCode: tangoerrors.ErrorUser,
		},
		{
			name:     "out-of-range is rejected",
			in:       tangopb.ComputationStrategy(99),
			wantErr:  true,
			wantCode: tangoerrors.ErrorUser,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateComputationStrategy(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, tangoerrors.GetErrorCode(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
