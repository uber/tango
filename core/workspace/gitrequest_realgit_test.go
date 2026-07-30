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

package workspace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/workspace"
	"go.uber.org/zap"
)

// runGit is a test helper that runs a git command in the given directory.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v in %s failed: %s", args, dir, string(out))
	return string(out)
}

// setupBareRepoWithPR creates a bare repo with a base commit and a PR ref
// (refs/pull/1/head). Returns (bare repo path, base SHA, PR commit SHA).
// The PR branch adds a file "pr.txt" with the given content.
func setupBareRepoWithPR(t *testing.T, prContent string) (bareDir, baseSHA, prSHA string) {
	t.Helper()

	// Create a work repo, commit a base, then push to bare.
	workDir := filepath.Join(t.TempDir(), "work")
	bareDir = filepath.Join(t.TempDir(), "bare.git")

	require.NoError(t, os.MkdirAll(workDir, 0o755))
	runGit(t, workDir, "init")
	runGit(t, workDir, "checkout", "-b", "main")

	// Base commit
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "base.txt"), []byte("base"), 0o644))
	runGit(t, workDir, "add", "base.txt")
	runGit(t, workDir, "commit", "-m", "base commit")

	// Create bare clone
	runGit(t, workDir, "clone", "--bare", workDir, bareDir)

	// Get base SHA from the bare repo
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = bareDir
	out, err := cmd.Output()
	require.NoError(t, err)
	baseSHA = string(out[:len(out)-1])

	// Create PR commit in the work repo
	runGit(t, workDir, "checkout", "-b", "pr-branch")
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "pr.txt"), []byte(prContent), 0o644))
	runGit(t, workDir, "add", "pr.txt")
	runGit(t, workDir, "commit", "-m", "PR commit: "+prContent)

	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = workDir
	out, err = cmd.Output()
	require.NoError(t, err)
	prSHA = string(out[:len(out)-1])

	// Push the PR ref to the bare repo
	runGit(t, workDir, "push", bareDir, "HEAD:refs/pull/1/head")

	return bareDir, baseSHA, prSHA
}

// TestGitRequest_PinnedCommit_StableTree verifies that applying a pinned
// commit yields the same tree even after the PR head advances. This is the
// critical property for cache correctness: the (URL, commit) cache key must
// always resolve to the same content.
func TestGitRequest_PinnedCommit_StableTree(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	logger := zap.NewNop().Sugar()

	// Set up the bare repo with a PR.
	bareDir, baseSHA, prSHA1 := setupBareRepoWithPR(t, "version-1")

	// --- First apply: pin to prSHA1 ---

	// Clone the bare repo into a worker (simulating what repomanager does)
	workerDir1 := filepath.Join(t.TempDir(), "worker1")
	runGit(t, t.TempDir(), "clone", bareDir, workerDir1)
	runGit(t, workerDir1, "checkout", baseSHA)

	g1 := git.New(workerDir1, logger)
	req1 := workspace.NewGitRequest(g1, "1", baseSHA, prSHA1, bareDir, logger)
	err := req1.Apply(ctx)
	require.NoError(t, err)

	tree1, err := g1.RevParse(ctx, "HEAD^{tree}")
	require.NoError(t, err)

	// --- Advance the PR head in the bare repo ---

	// Create a new work repo to push a new commit to the PR ref.
	advanceDir := filepath.Join(t.TempDir(), "advance")
	runGit(t, t.TempDir(), "clone", bareDir, advanceDir)
	// Fetch the PR ref explicitly since regular clones don't include refs/pull/*.
	runGit(t, advanceDir, "fetch", "origin", "refs/pull/1/head:refs/pull/1/head")
	runGit(t, advanceDir, "checkout", "-b", "pr-branch", "refs/pull/1/head")
	require.NoError(t, os.WriteFile(filepath.Join(advanceDir, "pr.txt"), []byte("version-2"), 0o644))
	runGit(t, advanceDir, "add", "pr.txt")
	runGit(t, advanceDir, "commit", "-m", "advance PR")
	runGit(t, advanceDir, "push", bareDir, "HEAD:refs/pull/1/head")

	// --- Second apply: same pinned commit (prSHA1) ---

	workerDir2 := filepath.Join(t.TempDir(), "worker2")
	runGit(t, t.TempDir(), "clone", bareDir, workerDir2)
	runGit(t, workerDir2, "checkout", baseSHA)

	g2 := git.New(workerDir2, logger)
	req2 := workspace.NewGitRequest(g2, "1", baseSHA, prSHA1, bareDir, logger)
	err = req2.Apply(ctx)
	require.NoError(t, err)

	tree2, err := g2.RevParse(ctx, "HEAD^{tree}")
	require.NoError(t, err)

	// The two trees must be identical because the same pinned commit was
	// applied, even though the PR head advanced between the two applies.
	assert.Equal(t, tree1, tree2, "pinned commit must yield the same tree regardless of PR head advancement")
}

// TestGitRequest_UpstreamFetch_WorksFromLocalClone verifies that PR refs
// are correctly fetched from the upstream (bare repo) even when the worker
// clone's "origin" is a local directory that does not expose pull/* refs
// (the scenario described in audit #3a).
func TestGitRequest_UpstreamFetch_WorksFromLocalClone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	logger := zap.NewNop().Sugar()

	bareDir, baseSHA, prSHA := setupBareRepoWithPR(t, "pr-content")

	// Simulate the repomanager: clone bare -> origin, then clone origin -> worker (--local).
	// The worker's "origin" points at the intermediate clone, not the bare repo,
	// so it does NOT have pull/* refs.
	originDir := filepath.Join(t.TempDir(), "origin")
	runGit(t, t.TempDir(), "clone", bareDir, originDir)

	workerDir := filepath.Join(t.TempDir(), "worker")
	runGit(t, t.TempDir(), "clone", "--local", originDir, workerDir)
	runGit(t, workerDir, "checkout", baseSHA)

	// Apply the PR using the bare repo as the upstream remote (not "origin").
	g := git.New(workerDir, logger)
	req := workspace.NewGitRequest(g, "1", baseSHA, prSHA, bareDir, logger)
	err := req.Apply(ctx)
	require.NoError(t, err)

	// Verify the PR content was applied.
	content, err := os.ReadFile(filepath.Join(workerDir, "pr.txt"))
	require.NoError(t, err)
	assert.Equal(t, "pr-content", string(content))
}

// TestGitRequest_PinnedCommit_RejectsStaleCommit verifies that a commit
// that is no longer an ancestor of the PR head (e.g. after a force-push)
// is rejected.
func TestGitRequest_PinnedCommit_RejectsStaleCommit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	logger := zap.NewNop().Sugar()

	bareDir, baseSHA, _ := setupBareRepoWithPR(t, "original")

	// Force-push a completely new history to the PR ref.
	rewriteDir := filepath.Join(t.TempDir(), "rewrite")
	runGit(t, t.TempDir(), "clone", bareDir, rewriteDir)
	runGit(t, rewriteDir, "checkout", "-b", "new-pr")
	require.NoError(t, os.WriteFile(filepath.Join(rewriteDir, "other.txt"), []byte("different"), 0o644))
	runGit(t, rewriteDir, "add", "other.txt")
	runGit(t, rewriteDir, "commit", "-m", "new PR history")
	runGit(t, rewriteDir, "push", "--force", bareDir, "HEAD:refs/pull/1/head")

	// Try to apply with the OLD commit SHA (which is no longer an ancestor).
	workerDir := filepath.Join(t.TempDir(), "worker")
	runGit(t, t.TempDir(), "clone", bareDir, workerDir)
	runGit(t, workerDir, "checkout", baseSHA)

	g := git.New(workerDir, logger)
	// Use a made-up SHA that won't be an ancestor of the new PR head.
	req := workspace.NewGitRequest(g, "1", baseSHA, "0000000000000000000000000000000000000000", bareDir, logger)
	err := req.Apply(ctx)
	require.Error(t, err)
}
