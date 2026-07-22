package mapper

import (
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
