package mapper

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
					{Url: "https://example.com/pr/1", Commit: "sha1"},
					{Url: "https://example.com/pr/2", Commit: "sha2"},
				},
				Strategy: tangopb.COMPUTATION_STRATEGY_NATIVE,
			},
			want: entity.BuildDescription{
				Remote:  "git@example.com:org/repo",
				BaseSha: "abc123",
				ChangeRequests: []entity.ChangeRequest{
					{URL: "https://example.com/pr/1", Commit: "sha1"},
					{URL: "https://example.com/pr/2", Commit: "sha2"},
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

func TestToComputationStrategy(t *testing.T) {
	tests := []struct {
		name string
		in   tangopb.ComputationStrategy
		want entity.ComputationStrategy
	}{
		{"unset", tangopb.COMPUTATION_STRATEGY_UNSET, entity.ComputationStrategyUnset},
		{"shell", tangopb.COMPUTATION_STRATEGY_SHELL, entity.ComputationStrategyShell},
		{"native", tangopb.COMPUTATION_STRATEGY_NATIVE, entity.ComputationStrategyNative},
		{"invalid", tangopb.COMPUTATION_STRATEGY_INVALID, entity.ComputationStrategyInvalid},
		{"out-of-range", tangopb.ComputationStrategy(99), entity.ComputationStrategyInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, toComputationStrategy(tt.in))
		})
	}
}
