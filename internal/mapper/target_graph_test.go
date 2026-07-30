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
				BuildDescription: &tangopb.BuildDescription{
					Remote:   "remote",
					BaseSha:  "abc123",
					Strategy: tangopb.COMPUTATION_STRATEGY_UNSET,
				},
				OutputConfig: &tangopb.OutputConfig{},
			},
			want: entity.GetTargetGraphRequest{
				Build: entity.BuildDescription{Remote: "remote", BaseSha: "abc123", Strategy: entity.ComputationStrategyUnset},
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

func TestGetTargetGraphResponseToProto_Targets(t *testing.T) {
	t.Parallel()

	chunk := entity.GetTargetGraphResponse{
		Targets: []entity.OptimizedTarget{
			{ID: 1, Hash: "ab", DirectDependencies: []int32{2}, RuleType: 10, Root: true},
			{ID: 2, Hash: "cd", Tags: []int32{5, 6}, External: true},
		},
	}

	proto := GetTargetGraphResponseToProto(&chunk)
	targets := proto.GetItem().(*tangopb.GetTargetGraphResponse_Targets)
	require.Len(t, targets.Targets.GetTargets(), 2)

	got := targets.Targets.GetTargets()[0]
	assert.Equal(t, int32(1), got.GetId())
	assert.Equal(t, "ab", got.GetHash())
	assert.Equal(t, []int32{2}, got.GetDirectDependencies())
	assert.Equal(t, int32(10), got.GetRuleType())
	assert.True(t, got.GetRoot())
}

func TestGetTargetGraphResponseToProto_Metadata(t *testing.T) {
	t.Parallel()

	chunk := entity.GetTargetGraphResponse{
		Metadata: &entity.Metadata{
			TargetIDMapping: map[int32]string{1: "//pkg:a", 2: "//pkg:b"},
			RuleTypeMapping: map[int32]string{10: "go_library"},
		},
	}

	proto := GetTargetGraphResponseToProto(&chunk)
	meta := proto.GetItem().(*tangopb.GetTargetGraphResponse_Metadata)
	assert.Equal(t, map[int32]string{1: "//pkg:a", 2: "//pkg:b"}, meta.Metadata.GetTargetIdMapping())
	assert.Equal(t, map[int32]string{10: "go_library"}, meta.Metadata.GetRuleTypeMapping())
}
