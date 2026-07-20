package mapper

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uber/tango/core/targethasher"
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

func TestResultToTargetGraph_EmptyResult(t *testing.T) {
	t.Parallel()

	targets, meta, err := ResultToTargetGraph(context.Background(), targethasher.Result{})
	require.NoError(t, err)
	assert.Empty(t, targets)
	assert.NotNil(t, meta)
}

func TestGetTargetGraphResponseToProto_RoundTrip(t *testing.T) {
	t.Parallel()

	chunk := entity.GetTargetGraphResponse{
		Targets: []entity.OptimizedTarget{
			{ID: 1, Hash: "ab", DirectDependencies: []int32{2}, RuleType: 10, Root: true},
			{ID: 2, Hash: "cd", Tags: []int32{5, 6}, External: true},
		},
	}

	proto := GetTargetGraphResponseToProto(&chunk)
	roundTripped := protoToEntityResponse(proto)
	assert.Equal(t, chunk, roundTripped)
}

func TestGetTargetGraphResponseToProto_Metadata_RoundTrip(t *testing.T) {
	t.Parallel()

	chunk := entity.GetTargetGraphResponse{
		Metadata: &entity.Metadata{
			TargetIDMapping: map[int32]string{1: "//pkg:a", 2: "//pkg:b"},
			RuleTypeMapping: map[int32]string{10: "go_library"},
		},
	}

	proto := GetTargetGraphResponseToProto(&chunk)
	roundTripped := protoToEntityResponse(proto)
	assert.Equal(t, chunk, roundTripped)
}

func protoToEntityResponse(resp *tangopb.GetTargetGraphResponse) entity.GetTargetGraphResponse {
	switch item := resp.GetItem().(type) {
	case *tangopb.GetTargetGraphResponse_Targets:
		targets := make([]entity.OptimizedTarget, len(item.Targets.GetTargets()))
		for i, t := range item.Targets.GetTargets() {
			targets[i] = protoToOptimizedTarget(t)
		}
		return entity.GetTargetGraphResponse{Targets: targets}
	case *tangopb.GetTargetGraphResponse_Metadata:
		return entity.GetTargetGraphResponse{Metadata: protoToMetadata(item.Metadata)}
	default:
		return entity.GetTargetGraphResponse{}
	}
}
