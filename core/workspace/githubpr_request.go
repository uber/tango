// Copyright (c) 2026 Uber Technologies, Inc.
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
	"github.com/uber/tango/core/workspace/githubpr"
	"go.uber.org/zap"
)

type githubPullRequest struct {
	git      git.Interface
	remote   string
	pr       githubpr.PullRequest
	baseRef  string
	logger   *zap.SugaredLogger
}

// newGitHubPRRequest creates a Request that applies a GitHub pull request.
func newGitHubPRRequest(git git.Interface, remote string, pr githubpr.PullRequest, baseRef string, logger *zap.SugaredLogger) Request {
	return &githubPullRequest{
		git:     git,
		remote:  remote,
		pr:      pr,
		baseRef: baseRef,
		logger:  logger,
	}
}

// Apply fetches the PR ref, verifies the pinned head SHA is reachable,
// diffs against it, and applies the patch to the workspace.
func (r *githubPullRequest) Apply(ctx context.Context) error {
	r.logger.Infow("Applying GitHub PR", zap.String("pr_number", r.pr.Number), zap.String("base_ref", r.baseRef), zap.String("head_sha", r.pr.HeadSHA))
	ref := fmt.Sprintf("+pull/%s/head:pull/%s/head", r.pr.Number, r.pr.Number)
	err := r.git.Fetch(ctx, r.remote, ref, "--force", "--no-tags")
	if err != nil {
		return fmt.Errorf("fetch PR %s: %w", r.pr.Number, err)
	}
	prRef := fmt.Sprintf("pull/%s/head", r.pr.Number)
	isAncestor, err := r.git.IsAncestor(ctx, r.pr.HeadSHA, prRef)
	if err != nil {
		return fmt.Errorf("failed to read PR commit history: %w", err)
	}
	if !isAncestor {
		return fmt.Errorf("head SHA %q is not an ancestor of PR %s", r.pr.HeadSHA, r.pr.Number)
	}
	patch, err := r.git.Diff(ctx, r.baseRef, r.pr.HeadSHA, "--binary", "--merge-base")
	if err != nil {
		return fmt.Errorf("compute diff for PR %s: %w", r.pr.Number, err)
	}
	err = r.git.ApplyPatch(ctx, patch)
	if err != nil {
		return fmt.Errorf("apply patch for PR %s: %w", r.pr.Number, err)
	}
	err = r.git.Commit(ctx, fmt.Sprintf("Applied PR: %s", r.pr.Number), "--allow-empty")
	if err != nil {
		return fmt.Errorf("commit PR %s: %w", r.pr.Number, err)
	}
	err = r.git.SubmoduleUpdate(ctx)
	if err != nil {
		return fmt.Errorf("update submodules for PR %s: %w", r.pr.Number, err)
	}
	r.logger.Infow("Successfully applied GitHub PR", zap.String("pr_number", r.pr.Number))
	return nil
}
