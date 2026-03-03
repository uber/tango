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
	"fmt"
	"path/filepath"

	"github.com/uber/tango/core/git"
)

type gitRequest struct {
	git       git.Interface
	requestID string
	baseRef   string
	commit    string
}

func NewGitRequest(git git.Interface, requestPath string, baseRef string, commit string) Request {
	// get the last part of the request path
	requestID := filepath.Base(requestPath)
	return &gitRequest{
		git:       git,
		requestID: requestID,
		baseRef:   baseRef,
		commit:    commit,
	}
}

// Apply applies the change request to the workspace.
func (r *gitRequest) Apply(ctx context.Context) error {
	ref := fmt.Sprintf("+pull/%s/head:pull/%s/head", r.requestID, r.requestID)
	err := r.git.Fetch(ctx, "origin", ref, "--force", "--no-tags")
	if err != nil {
		return err
	}
	if r.commit != "" {
		isAncestor, err := r.git.IsAncestor(ctx, r.commit, fmt.Sprintf("pull/%s/head", r.requestID))
		if err != nil {
			return fmt.Errorf("failed to read PR commit history: %w", err)
		}
		if !isAncestor {
			return fmt.Errorf("commit %q is not an ancestor of PR %s", r.commit, r.requestID)
		}
	}
	patch, err := r.git.Diff(ctx, r.baseRef, fmt.Sprintf("pull/%s/head", r.requestID), "--binary", "--merge-base")
	if err != nil {
		return err
	}
	err = r.git.ApplyPatch(ctx, patch)
	if err != nil {
		return err
	}
	err = r.git.Commit(ctx, fmt.Sprintf("Applied PR: %s", r.requestID), "--allow-empty")
	if err != nil {
		return err
	}
	err = r.git.SubmoduleUpdate(ctx)
	if err != nil {
		return err
	}
	return nil
}
