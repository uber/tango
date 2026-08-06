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
	"github.com/uber/tango/core/workspace/githubpr"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
)

func testPR(number, headSHA string) githubpr.PullRequest {
	return githubpr.PullRequest{
		Host: "github.com", Org: "org", Repo: "repo",
		Number: number, HeadSHA: headSHA,
	}
}

func TestGitHubPRRequest_Fields(t *testing.T) {
	pr := testPR("456", _testHeadSHA)
	r := newGitHubPRRequest(nil, "https://github.com/org/repo", pr, "baseRef", zap.NewNop().Sugar())
	gr, ok := r.(*githubPullRequest)
	assert.True(t, ok, "expected *githubPullRequest, got %T", r)
	assert.Equal(t, "https://github.com/org/repo", gr.remote)
	assert.Equal(t, "456", gr.pr.Number)
	assert.Equal(t, "baseRef", gr.baseRef)
	assert.Equal(t, _testHeadSHA, gr.pr.HeadSHA)
}

func TestGitHubPRRequest_Apply_HeadSHAIsAncestor_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)
	git.EXPECT().Fetch(gomock.Any(), "https://github.com/org/repo", gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().IsAncestor(gomock.Any(), "deadbeef", "pull/123/head").Return(true, nil)
	git.EXPECT().Diff(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
	git.EXPECT().ApplyPatch(gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().Commit(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().SubmoduleUpdate(gomock.Any()).Return(nil)
	req := newGitHubPRRequest(git, "https://github.com/org/repo", testPR("123", "deadbeef"), "baseRef", zap.NewNop().Sugar())
	err := req.Apply(context.Background())
	require.NoError(t, err)
}

func TestGitHubPRRequest_Apply_HeadSHANotAncestor_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)
	git.EXPECT().Fetch(gomock.Any(), "https://github.com/org/repo", gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().IsAncestor(gomock.Any(), "deadbeef", "pull/456/head").Return(false, nil)
	req := newGitHubPRRequest(git, "https://github.com/org/repo", testPR("456", "deadbeef"), "baseRef", zap.NewNop().Sugar())
	err := req.Apply(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deadbeef")
}

func TestGitHubPRRequest_Apply_IsAncestorFails_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)
	git.EXPECT().Fetch(gomock.Any(), "https://github.com/org/repo", gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().IsAncestor(gomock.Any(), "deadbeef", "pull/789/head").Return(false, errors.New("ancestor check failed"))
	req := newGitHubPRRequest(git, "https://github.com/org/repo", testPR("789", "deadbeef"), "baseRef", zap.NewNop().Sugar())
	err := req.Apply(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read PR commit history")
}

func TestGitHubPRRequest_Apply_DiffsAgainstPinnedHeadSHA(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)
	git.EXPECT().Fetch(gomock.Any(), "https://github.com/org/repo", gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().IsAncestor(gomock.Any(), "pinnedsha", "pull/10/head").Return(true, nil)
	git.EXPECT().Diff(gomock.Any(), "baseRef", "pinnedsha", "--binary", "--merge-base").Return([]byte("patch"), nil)
	git.EXPECT().ApplyPatch(gomock.Any(), []byte("patch")).Return(nil)
	git.EXPECT().Commit(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().SubmoduleUpdate(gomock.Any()).Return(nil)
	req := newGitHubPRRequest(git, "https://github.com/org/repo", testPR("10", "pinnedsha"), "baseRef", zap.NewNop().Sugar())
	err := req.Apply(context.Background())
	require.NoError(t, err)
}
