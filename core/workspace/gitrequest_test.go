package workspace

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitmock "github.com/uber/tango/core/git/gitmock"
	"go.uber.org/mock/gomock"
)

func TestNewGitRequest_InvalidPath(t *testing.T) {
	req := NewGitRequest(nil, "invalid", "baseRef", "")
	require.NotNil(t, req)
}

func TestNewGitRequest_ExtractsID(t *testing.T) {
	r := NewGitRequest(nil, "/org/repo/pull/456", "baseRef", "abc123")
	gr, ok := r.(*gitRequest)
	assert.True(t, ok, "expected *gitRequest, got %T", r)
	assert.Equal(t, "456", gr.requestID)
	assert.Equal(t, "baseRef", gr.baseRef)
	assert.Equal(t, "abc123", gr.commit)
}

func TestGitRequest_Apply_EmptyCommit_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)
	git.EXPECT().Fetch(gomock.Any(), "origin", gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().RevParse(gomock.Any(), "pull/123/head").Return("deadbeef\n", nil)
	req := NewGitRequest(git, "123", "baseRef", "")
	err := req.Apply(context.Background())
	require.Error(t, err)
}

func TestGitRequest_Apply_CommitMatches_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)
	git.EXPECT().Fetch(gomock.Any(), "origin", gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().RevParse(gomock.Any(), "pull/123/head").Return("deadbeef\n", nil)
	git.EXPECT().Diff(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
	git.EXPECT().ApplyPatch(gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().Commit(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().SubmoduleUpdate(gomock.Any()).Return(nil)
	req := NewGitRequest(git, "123", "baseRef", "deadbeef")
	err := req.Apply(context.Background())
	require.NoError(t, err)
}

func TestGitRequest_Apply_CommitMismatch_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)
	git.EXPECT().Fetch(gomock.Any(), "origin", gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().RevParse(gomock.Any(), "pull/456/head").Return("aabbccdd\n", nil)
	req := NewGitRequest(git, "456", "baseRef", "deadbeef")
	err := req.Apply(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aabbccdd")
	assert.Contains(t, err.Error(), "deadbeef")
}

func TestGitRequest_Apply_RevParseFails_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)
	git.EXPECT().Fetch(gomock.Any(), "origin", gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().RevParse(gomock.Any(), "pull/789/head").Return("", errors.New("rev-parse failed"))
	req := NewGitRequest(git, "789", "baseRef", "deadbeef")
	err := req.Apply(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to resolve PR head")
}
