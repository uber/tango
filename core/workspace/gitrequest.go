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
	"go.uber.org/zap"
)

type gitRequest struct {
	git            git.Interface
	requestID      string
	baseRef        string
	commit         string
	upstreamRemote string
	logger         *zap.SugaredLogger
}

// NewGitRequest creates a Request that applies a GitHub pull request.
//
// upstreamRemote is the real remote URL (e.g. the GitHub HTTPS/SSH URL)
// from which PR refs are fetched. Worker clones created with --local have
// their "origin" pointing at the pool's local origin directory, which does
// not expose pull/* refs, so fetches must target the upstream directly.
//
// commit pins the content that is applied: the diff is computed between
// baseRef and commit (not the floating PR head), and commit must be an
// ancestor of the current PR head as a sanity check. This ensures the
// materialized tree is stable for a given (URL, commit) cache key even as
// the PR advances.
func NewGitRequest(git git.Interface, requestPath string, baseRef string, commit string, upstreamRemote string, logger *zap.SugaredLogger) Request {
	// get the last part of the request path
	requestID := filepath.Base(requestPath)
	return &gitRequest{
		git:            git,
		requestID:      requestID,
		baseRef:        baseRef,
		commit:         commit,
		upstreamRemote: upstreamRemote,
		logger:         logger,
	}
}

// Apply applies the change request to the workspace.
//
// PR refs are fetched from upstreamRemote (the real GitHub URL) rather than
// "origin", because worker clones created with --local have their origin
// pointing at the pool's local directory which lacks pull/* refs.
//
// The diff is computed between baseRef and the pinned commit (not the
// floating PR head). The commit must be an ancestor of the current PR head
// as a sanity check, but the actual content applied is always the pinned
// commit so the materialized tree is deterministic for a given cache key.
func (r *gitRequest) Apply(ctx context.Context) error {
	r.logger.Infow("gitRequest: Applying PR",
		zap.String("request_id", r.requestID),
		zap.String("base_ref", r.baseRef),
		zap.String("commit", r.commit),
		zap.String("upstream_remote", r.upstreamRemote),
	)

	// Fetch the PR head ref from the upstream remote (not "origin") so the
	// ancestor check can verify the pinned commit belongs to this PR.
	prRef := fmt.Sprintf("pull/%s/head", r.requestID)
	fetchRef := fmt.Sprintf("+refs/%s:refs/%s", prRef, prRef)
	err := r.git.Fetch(ctx, r.upstreamRemote, fetchRef, "--force", "--no-tags")
	if err != nil {
		return fmt.Errorf("fetch PR %s from upstream: %w", r.requestID, err)
	}

	// Check whether the pinned commit object is already present locally.
	// It almost always will be, since commit must be an ancestor of the PR
	// head we just fetched. Only when the object is missing (e.g. shallow
	// clone, or unusual ref topology) do we attempt a bare-SHA fetch as a
	// best-effort fallback. Many git servers refuse bare-SHA fetches unless
	// uploadpack.allowReachableSHA1InWant is enabled, so this path must not
	// be required for the normal case.
	if _, revErr := r.git.RevParse(ctx, r.commit+"^{commit}"); revErr != nil {
		r.logger.Infow("gitRequest: pinned commit not found locally, attempting bare-SHA fetch",
			zap.String("commit", r.commit),
			zap.Error(revErr),
		)
		if fetchErr := r.git.Fetch(ctx, r.upstreamRemote, r.commit, "--force", "--no-tags"); fetchErr != nil {
			return fmt.Errorf("pinned commit %s is not available locally or from upstream: %w", r.commit, fetchErr)
		}
	}

	// Sanity-check: the pinned commit must be an ancestor of the current PR
	// head. This catches stale or bogus commit values without silently
	// applying unrelated content.
	isAncestor, err := r.git.IsAncestor(ctx, r.commit, prRef)
	if err != nil {
		return fmt.Errorf("failed to read PR commit history: %w", err)
	}
	if !isAncestor {
		return fmt.Errorf("commit %q is not an ancestor of PR %s", r.commit, r.requestID)
	}

	// Diff against the pinned commit, not the floating PR head. This makes
	// the materialized tree deterministic for the (URL, commit) cache key.
	patch, err := r.git.Diff(ctx, r.baseRef, r.commit, "--binary", "--merge-base")
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
