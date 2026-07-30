// Copyright (c) 2025 Uber Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package workspace

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitmock "github.com/uber/tango/core/git/gitmock"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
)

const _testUpstream = "git@github.com:org/repo"

func TestNewGitRequest_InvalidPath(t *testing.T) {
	req := NewGitRequest(nil, "invalid", "baseRef", "", _testUpstream, zap.NewNop().Sugar())
	require.NotNil(t, req)
}

func TestNewGitRequest_ExtractsID(t *testing.T) {
	r := NewGitRequest(nil, "/org/repo/pull/456", "baseRef", "abc123", _testUpstream, zap.NewNop().Sugar())
	gr, ok := r.(*gitRequest)
	assert.True(t, ok, "expected *gitRequest, got %T", r)
	assert.Equal(t, "456", gr.requestID)
	assert.Equal(t, "baseRef", gr.baseRef)
	assert.Equal(t, "abc123", gr.commit)
	assert.Equal(t, _testUpstream, gr.upstreamRemote)
}

func TestGitRequest_Apply_CommitIsAncestor_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)

	// Fetch PR head from upstream remote (not "origin")
	git.EXPECT().Fetch(gomock.Any(), _testUpstream, "+refs/pull/123/head:refs/pull/123/head", "--force", "--no-tags").Return(nil)
	// Fetch pinned commit from upstream
	git.EXPECT().Fetch(gomock.Any(), _testUpstream, "deadbeef", "--force", "--no-tags").Return(nil)
	git.EXPECT().IsAncestor(gomock.Any(), "deadbeef", "pull/123/head").Return(true, nil)
	// Diff against pinned commit (not PR head)
	git.EXPECT().Diff(gomock.Any(), "baseRef", "deadbeef", "--binary", "--merge-base").Return(nil, nil)
	git.EXPECT().ApplyPatch(gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().Commit(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().SubmoduleUpdate(gomock.Any()).Return(nil)

	req := NewGitRequest(git, "123", "baseRef", "deadbeef", _testUpstream, zap.NewNop().Sugar())
	err := req.Apply(context.Background())
	require.NoError(t, err)
}

func TestGitRequest_Apply_CommitNotAncestor_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)

	git.EXPECT().Fetch(gomock.Any(), _testUpstream, "+refs/pull/456/head:refs/pull/456/head", "--force", "--no-tags").Return(nil)
	git.EXPECT().Fetch(gomock.Any(), _testUpstream, "deadbeef", "--force", "--no-tags").Return(nil)
	git.EXPECT().IsAncestor(gomock.Any(), "deadbeef", "pull/456/head").Return(false, nil)

	req := NewGitRequest(git, "456", "baseRef", "deadbeef", _testUpstream, zap.NewNop().Sugar())
	err := req.Apply(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deadbeef")
}

func TestGitRequest_Apply_IsAncestorFails_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)

	git.EXPECT().Fetch(gomock.Any(), _testUpstream, "+refs/pull/789/head:refs/pull/789/head", "--force", "--no-tags").Return(nil)
	git.EXPECT().Fetch(gomock.Any(), _testUpstream, "deadbeef", "--force", "--no-tags").Return(nil)
	git.EXPECT().IsAncestor(gomock.Any(), "deadbeef", "pull/789/head").Return(false, errors.New("ancestor check failed"))

	req := NewGitRequest(git, "789", "baseRef", "deadbeef", _testUpstream, zap.NewNop().Sugar())
	err := req.Apply(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read PR commit history")
}

func TestGitRequest_Apply_FetchFromUpstream(t *testing.T) {
	// Verifies that PR refs are fetched from the upstream remote, not "origin".
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)

	upstream := "https://github.com/myorg/myrepo.git"
	git.EXPECT().Fetch(gomock.Any(), upstream, "+refs/pull/42/head:refs/pull/42/head", "--force", "--no-tags").Return(nil)
	git.EXPECT().Fetch(gomock.Any(), upstream, "abc123", "--force", "--no-tags").Return(nil)
	git.EXPECT().IsAncestor(gomock.Any(), "abc123", "pull/42/head").Return(true, nil)
	git.EXPECT().Diff(gomock.Any(), "main", "abc123", "--binary", "--merge-base").Return(nil, nil)
	git.EXPECT().ApplyPatch(gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().Commit(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().SubmoduleUpdate(gomock.Any()).Return(nil)

	req := NewGitRequest(git, "42", "main", "abc123", upstream, zap.NewNop().Sugar())
	err := req.Apply(context.Background())
	require.NoError(t, err)
}

func TestGitRequest_Apply_PinsToDiffAgainstCommit(t *testing.T) {
	// Verifies that the diff is computed against the pinned commit, not the
	// floating PR head, ensuring stable tree materialization.
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)

	git.EXPECT().Fetch(gomock.Any(), _testUpstream, "+refs/pull/10/head:refs/pull/10/head", "--force", "--no-tags").Return(nil)
	git.EXPECT().Fetch(gomock.Any(), _testUpstream, "pinnedSHA", "--force", "--no-tags").Return(nil)
	git.EXPECT().IsAncestor(gomock.Any(), "pinnedSHA", "pull/10/head").Return(true, nil)
	// The key assertion: diff target is "pinnedSHA", not "pull/10/head"
	git.EXPECT().Diff(gomock.Any(), "baseRef", "pinnedSHA", "--binary", "--merge-base").Return([]byte("patch"), nil)
	git.EXPECT().ApplyPatch(gomock.Any(), []byte("patch")).Return(nil)
	git.EXPECT().Commit(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().SubmoduleUpdate(gomock.Any()).Return(nil)

	req := NewGitRequest(git, "10", "baseRef", "pinnedSHA", _testUpstream, zap.NewNop().Sugar())
	err := req.Apply(context.Background())
	require.NoError(t, err)
}

func TestGitRequest_Apply_FetchPRHeadFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)

	git.EXPECT().Fetch(gomock.Any(), _testUpstream, gomock.Any(), "--force", "--no-tags").Return(errors.New("network error"))

	req := NewGitRequest(git, "99", "baseRef", "abc", _testUpstream, zap.NewNop().Sugar())
	err := req.Apply(context.Background())
	require.Error(t, err)
}

func TestGitRequest_Apply_FetchPinnedCommitFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)

	git.EXPECT().Fetch(gomock.Any(), _testUpstream, "+refs/pull/99/head:refs/pull/99/head", "--force", "--no-tags").Return(nil)
	git.EXPECT().Fetch(gomock.Any(), _testUpstream, "abc", "--force", "--no-tags").Return(errors.New("commit not found"))

	req := NewGitRequest(git, "99", "baseRef", "abc", _testUpstream, zap.NewNop().Sugar())
	err := req.Apply(context.Background())
	require.Error(t, err)
}
