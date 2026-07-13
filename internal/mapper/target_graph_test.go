package mapper

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/tangopb"
)

func TestToGetTargetGraphRequest(t *testing.T) {
	tests := []struct {
		name string
		req  *tangopb.GetTargetGraphRequest
		want entity.GetTargetGraphRequest
	}{
		{
			name: "nil",
			req:  nil,
			want: entity.GetTargetGraphRequest{},
		},
		{
			name: "empty",
			req:  &tangopb.GetTargetGraphRequest{},
			want: entity.GetTargetGraphRequest{
				Build: entity.BuildDescription{},
			},
		},
		{
			name: "full",
			req: &tangopb.GetTargetGraphRequest{
				BuildDescription: &tangopb.BuildDescription{
					Remote:   "gitolite@code.uber.internal:go-code",
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
					Remote:   "gitolite@code.uber.internal:go-code",
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
				BuildDescription: &tangopb.BuildDescription{Remote: "remote"},
				OutputConfig:     &tangopb.OutputConfig{},
			},
			want: entity.GetTargetGraphRequest{
				Build: entity.BuildDescription{Remote: "remote"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToGetTargetGraphRequest(tt.req)
			assert.Equal(t, tt.want, got)
		})
	}
}
