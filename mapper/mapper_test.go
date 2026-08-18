package mapper

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uber/tango/core/targethasher"
)

func TestResultToTargetGraph_EmptyResult(t *testing.T) {
	t.Parallel()

	targets, meta, err := ResultToTargetGraph(t.Context(), targethasher.Result{})
	require.NoError(t, err)
	assert.Empty(t, targets)
	assert.NotNil(t, meta)
}

func TestResultToTargetGraph_PropagatesAllTargetsFileHashes(t *testing.T) {
	t.Parallel()

	hashes := map[string]string{".bazelrc": "abc123", "tools/bazel": "def456"}
	result := targethasher.Result{
		AllTargetsFileHashes: hashes,
	}

	_, meta, err := ResultToTargetGraph(t.Context(), result)
	require.NoError(t, err)
	assert.Equal(t, hashes, meta.AllTargetsFileHashes)
}

func TestResultToTargetGraph_NilAllTargetsFileHashes(t *testing.T) {
	t.Parallel()

	_, meta, err := ResultToTargetGraph(t.Context(), targethasher.Result{})
	require.NoError(t, err)
	assert.Nil(t, meta.AllTargetsFileHashes)
}

func TestResultToGraphChunks(t *testing.T) {
	t.Parallel()

	result := targethasher.Result{
		TargetNames: []string{"//a:a", "//b:b"},
		Targets: map[string]*targethasher.Target{
			"//a:a": {Hash: []byte{0x01}, RuleType: "go_library", Deps: []string{"//b:b"}},
			"//b:b": {Hash: []byte{0x02}, RuleType: "go_library"},
		},
	}

	t.Run("single chunk when under budget", func(t *testing.T) {
		t.Parallel()

		chunks, err := ResultToGraphChunks(t.Context(), result, 1<<20)
		require.NoError(t, err)

		var targets, metas int
		for _, c := range chunks {
			targets += len(c.Targets)
			if c.Metadata != nil {
				metas++
			}
		}
		assert.Equal(t, 2, targets)
		assert.Positive(t, metas, "expected at least one metadata chunk")
	})

	t.Run("splits targets across chunks under a tiny budget", func(t *testing.T) {
		t.Parallel()

		chunks, err := ResultToGraphChunks(t.Context(), result, 1)
		require.NoError(t, err)

		targetChunks := 0
		total := 0
		for _, c := range chunks {
			if len(c.Targets) > 0 {
				targetChunks++
				total += len(c.Targets)
			}
		}
		assert.Equal(t, 2, total)
		assert.Greater(t, targetChunks, 1, "tiny budget should spread targets over multiple chunks")
	})

	t.Run("propagates context cancellation", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := ResultToGraphChunks(ctx, result, 1<<20)
		require.Error(t, err)
	})
}
