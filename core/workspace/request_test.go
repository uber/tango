package workspace

import (
	"testing"

	"github.com/uber/tango/core/git"
	"github.com/stretchr/testify/require"
)

func TestNewRequest_Github_Success(t *testing.T) {
	rawURL := "github://org/repo/pull/123"
	var g git.Interface = nil

	req, err := NewRequest(rawURL, g, "baseRef", "abc123")
	require.NoError(t, err)
	require.NotNil(t, req)

	gr, ok := req.(*gitRequest)
	require.True(t, ok, "returned Request should be *gitRequest")
	require.Equal(t, "123", gr.requestID)
	require.Equal(t, "abc123", gr.commit)
	require.Nil(t, gr.git)
}


func TestNewRequest_InvalidURL(t *testing.T) {
	rawURL := "://bad"
	var g git.Interface = nil

	req, err := NewRequest(rawURL, g, "baseRef", "")
	require.Error(t, err)
	require.Nil(t, req)
}
