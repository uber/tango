package mapper

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/tangopb"
)

func TestProtoToGetTargetGraphRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     *tangopb.GetTargetGraphRequest
		want    entity.GetTargetGraphRequest
		wantErr bool
	}{
		{
			name:    "nil",
			req:     nil,
			wantErr: true,
		},
		{
			name:    "empty",
			req:     &tangopb.GetTargetGraphRequest{},
			wantErr: true,
		},
		{
			name:    "missing build description",
			req:     &tangopb.GetTargetGraphRequest{RequestOptions: &tangopb.RequestOptions{}},
			wantErr: true,
		},
		{
			name: "invalid build description",
			req: &tangopb.GetTargetGraphRequest{
				BuildDescription: &tangopb.BuildDescription{Remote: "remote"},
			},
			wantErr: true,
		},
		{
			name: "full",
			req: &tangopb.GetTargetGraphRequest{
				BuildDescription: &tangopb.BuildDescription{
					Remote:   "git@example.com:org/repo",
					BaseSha:  "abc123",
					Strategy: tangopb.COMPUTATION_STRATEGY_SHELL,
				},
				RequestOptions: &tangopb.RequestOptions{
					ExtraExcludeFilesRegex: []string{"foo.*", "bar.*"},
				},
				BypassCache: true,
			},
			want: entity.GetTargetGraphRequest{
				Build: entity.BuildDescription{
					Remote:   "git@example.com:org/repo",
					BaseSha:  "abc123",
					Strategy: entity.ComputationStrategyShell,
				},
				ExcludeFilesRegex: []string{"foo.*", "bar.*"},
				BypassCache:       true,
			},
		},
		{
			name: "output config is dropped",
			req: &tangopb.GetTargetGraphRequest{
				BuildDescription: &tangopb.BuildDescription{Remote: "remote", BaseSha: "abc123"},
				OutputConfig:     &tangopb.OutputConfig{},
			},
			want: entity.GetTargetGraphRequest{
				Build: entity.BuildDescription{Remote: "remote", BaseSha: "abc123", Strategy: entity.ComputationStrategyInvalid},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ProtoToGetTargetGraphRequest(tt.req)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
