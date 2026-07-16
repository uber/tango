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

func TestResultToTargetGraph(t *testing.T) {
	t.Parallel()

	result := targethasher.Result{
		TargetNames: []string{"//pkg:a", "//pkg:b"},
		Targets: map[string]*targethasher.Target{
			"//pkg:a": {Name: "//pkg:a", Hash: []byte{0xab}, RuleType: "go_library", Deps: []string{"//pkg:b"}, Tags: []string{"manual"}, Root: true},
			"//pkg:b": {Name: "//pkg:b", Hash: []byte{0xcd}, RuleType: "go_test", External: true},
		},
	}

	graph, err := ResultToTargetGraph(context.Background(), result)
	require.NoError(t, err)
	require.Len(t, graph.Targets, 2)

	a := graph.Targets[0]
	assert.Equal(t, "//pkg:a", a.Name)
	assert.Equal(t, "ab", a.Hash)
	assert.Equal(t, "go_library", a.RuleType)
	assert.Equal(t, []string{"//pkg:b"}, a.Dependencies)
	assert.Equal(t, []string{"manual"}, a.Tags)
	assert.True(t, a.Root)
	assert.False(t, a.External)

	b := graph.Targets[1]
	assert.Equal(t, "//pkg:b", b.Name)
	assert.Equal(t, "cd", b.Hash)
	assert.True(t, b.External)
}

func TestResultToTargetGraph_EmptyResult(t *testing.T) {
	t.Parallel()

	graph, err := ResultToTargetGraph(context.Background(), targethasher.Result{})
	require.NoError(t, err)
	assert.Empty(t, graph.Targets)
}

func TestTargetGraphToProto_EmptyGraph(t *testing.T) {
	t.Parallel()

	responses, err := TargetGraphToProto(context.Background(), entity.TargetGraph{}, 1<<20)
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
