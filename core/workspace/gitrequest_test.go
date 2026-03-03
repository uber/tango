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


func TestGitRequest_Apply_CommitIsAncestor_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)
	git.EXPECT().Fetch(gomock.Any(), "origin", gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().IsAncestor(gomock.Any(), "deadbeef", "pull/123/head").Return(true, nil)
	git.EXPECT().Diff(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
	git.EXPECT().ApplyPatch(gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().Commit(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().SubmoduleUpdate(gomock.Any()).Return(nil)
	req := NewGitRequest(git, "123", "baseRef", "deadbeef")
	err := req.Apply(context.Background())
	require.NoError(t, err)
}

func TestGitRequest_Apply_CommitNotAncestor_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)
	git.EXPECT().Fetch(gomock.Any(), "origin", gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().IsAncestor(gomock.Any(), "deadbeef", "pull/456/head").Return(false, nil)
	req := NewGitRequest(git, "456", "baseRef", "deadbeef")
	err := req.Apply(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deadbeef")
}

func TestGitRequest_Apply_IsAncestorFails_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	git := gitmock.NewMockInterface(ctrl)
	git.EXPECT().Fetch(gomock.Any(), "origin", gomock.Any(), gomock.Any()).Return(nil)
	git.EXPECT().IsAncestor(gomock.Any(), "deadbeef", "pull/789/head").Return(false, errors.New("ancestor check failed"))
	req := NewGitRequest(git, "789", "baseRef", "deadbeef")
	err := req.Apply(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read PR commit history")
}
