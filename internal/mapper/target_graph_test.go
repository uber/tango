package mapper

import (
	"context"
	"fmt"
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

func TestResultToGetTargetGraphResponse_EmptyResult(t *testing.T) {
	t.Parallel()

	responses, err := ResultToGetTargetGraphResponse(context.Background(), targethasher.Result{}, 1<<20)
	require.NoError(t, err)
	require.Len(t, responses, 2)

	targets, ok := responses[0].Item.(*tangopb.GetTargetGraphResponse_Targets)
	require.True(t, ok)
	assert.Empty(t, targets.Targets.Targets)

	_, ok = responses[1].Item.(*tangopb.GetTargetGraphResponse_Metadata)
	assert.True(t, ok)
}

func TestResultToGetTargetGraphResponse_Chunking(t *testing.T) {
	t.Parallel()

	numTargets := 50
	result := targethasher.Result{
		TargetNames: make([]string, numTargets),
		Targets:     make(map[string]*targethasher.Target, numTargets),
	}
	for i := 0; i < numTargets; i++ {
		name := fmt.Sprintf("//pkg:target%d", i)
		result.TargetNames[i] = name
		result.Targets[name] = &targethasher.Target{Name: name, Hash: []byte{0}, RuleType: "go_library"}
	}

	baseline, err := ResultToGetTargetGraphResponse(context.Background(), result, 1<<30)
	require.NoError(t, err)
	var targetBytes int
	for _, resp := range baseline {
		if targets, ok := resp.Item.(*tangopb.GetTargetGraphResponse_Targets); ok {
			targetBytes = targets.Targets.Targets[0].Size()
			break
		}
	}
	require.NotZero(t, targetBytes)

	tests := []struct {
		name             string
		maxMessageBytes  int
		wantTargetChunks int
	}{
		{name: "25 per chunk", maxMessageBytes: targetBytes * 25, wantTargetChunks: 2},
		{name: "10 per chunk", maxMessageBytes: targetBytes * 10, wantTargetChunks: 5},
		{name: "all in one chunk", maxMessageBytes: targetBytes * 100, wantTargetChunks: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			responses, err := ResultToGetTargetGraphResponse(context.Background(), result, tt.maxMessageBytes)
			require.NoError(t, err)

			var targetChunks, totalTargets int
			for _, resp := range responses {
				if targets, ok := resp.Item.(*tangopb.GetTargetGraphResponse_Targets); ok {
					targetChunks++
					totalTargets += len(targets.Targets.Targets)
				}
			}
			assert.Equal(t, tt.wantTargetChunks, targetChunks)
			assert.Equal(t, numTargets, totalTargets)
		})
	}
}
