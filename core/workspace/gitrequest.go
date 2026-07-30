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

	"github.com/uber/tango/core/git"
	"go.uber.org/zap"
)

type gitRequest struct {
	git       git.Interface
	requestID string
	baseRef   string
	headSHA   string
	logger    *zap.SugaredLogger
}

// NewGitRequest creates a Request that applies a GitHub pull request.
// requestID and headSHA come from the parsed change URI; see
// https://github.com/uber/submitqueue/blob/main/doc/rfc/change-uri.md.
func NewGitRequest(git git.Interface, requestID string, baseRef string, headSHA string, logger *zap.SugaredLogger) Request {
	return &gitRequest{
		git:       git,
		requestID: requestID,
		baseRef:   baseRef,
		headSHA:   headSHA,
		logger:    logger,
	}
}

// Apply applies the change request to the workspace.
func (r *gitRequest) Apply(ctx context.Context) error {
	r.logger.Infow("gitRequest: Applying PR", zap.String("request_id", r.requestID), zap.String("base_ref", r.baseRef), zap.String("head_sha", r.headSHA))
	ref := fmt.Sprintf("+pull/%s/head:pull/%s/head", r.requestID, r.requestID)
	err := r.git.Fetch(ctx, "origin", ref, "--force", "--no-tags")
	if err != nil {
		return fmt.Errorf("fetch PR %s: %w", r.requestID, err)
	}
	isAncestor, err := r.git.IsAncestor(ctx, r.headSHA, fmt.Sprintf("pull/%s/head", r.requestID))
	if err != nil {
		return fmt.Errorf("failed to read PR commit history: %w", err)
	}
	if !isAncestor {
		return fmt.Errorf("head SHA %q is not an ancestor of PR %s", r.headSHA, r.requestID)
	}
	// Diff against the pinned head SHA so the materialized tree is
	// deterministic for a given change URI even as the PR advances.
	patch, err := r.git.Diff(ctx, r.baseRef, r.headSHA, "--binary", "--merge-base")
	if err != nil {
		return fmt.Errorf("compute diff for PR %s: %w", r.requestID, err)
	}
	err = r.git.ApplyPatch(ctx, patch)
	if err != nil {
		return fmt.Errorf("apply patch for PR %s: %w", r.requestID, err)
	}
	err = r.git.Commit(ctx, fmt.Sprintf("Applied PR: %s", r.requestID), "--allow-empty")
	if err != nil {
		return fmt.Errorf("commit PR %s: %w", r.requestID, err)
	}
	err = r.git.SubmoduleUpdate(ctx)
	if err != nil {
		return fmt.Errorf("update submodules for PR %s: %w", r.requestID, err)
	}
	r.logger.Infow("gitRequest: Successfully applied PR", zap.String("request_id", r.requestID))
	return nil
}
