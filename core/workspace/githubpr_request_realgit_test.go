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

package workspace_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/workspace"
	"go.uber.org/zap"
)

const _testSHALen = 40

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
	return strings.TrimSpace(string(out))
}

func changeURI(prNumber, headSHA string) string {
	return fmt.Sprintf("github://github.com/test/repo/pull/%s/%s", prNumber, headSHA)
}

func setupBareRepoWithPR(t *testing.T, prContent string) (bareDir, baseSHA, prSHA string) {
	t.Helper()

	workDir := filepath.Join(t.TempDir(), "work")
	bareDir = filepath.Join(t.TempDir(), "bare.git")

	require.NoError(t, os.MkdirAll(workDir, 0o755))
	runGit(t, workDir, "init", "-b", "main")

	require.NoError(t, os.WriteFile(filepath.Join(workDir, "base.txt"), []byte("base"), 0o644))
	runGit(t, workDir, "add", "base.txt")
	runGit(t, workDir, "commit", "-m", "base commit")
	baseSHA = runGit(t, workDir, "rev-parse", "HEAD")

	runGit(t, workDir, "checkout", "-b", "pr-branch")
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "pr.txt"), []byte(prContent), 0o644))
	runGit(t, workDir, "add", "pr.txt")
	runGit(t, workDir, "commit", "-m", "PR commit: "+prContent)
	prSHA = runGit(t, workDir, "rev-parse", "HEAD")

	runGit(t, t.TempDir(), "clone", "--bare", workDir, bareDir)
	runGit(t, workDir, "push", bareDir, "HEAD:refs/pull/1/head")
	runGit(t, bareDir, "branch", "-D", "pr-branch")

	return bareDir, baseSHA, prSHA
}

func cloneWorker(t *testing.T, bareDir, baseSHA string) string {
	t.Helper()
	workerDir := filepath.Join(t.TempDir(), "worker")
	runGit(t, t.TempDir(), "clone", bareDir, workerDir)
	runGit(t, workerDir, "checkout", baseSHA)
	runGit(t, workerDir, "config", "user.name", "test")
	runGit(t, workerDir, "config", "user.email", "test@test.com")
	return workerDir
}

func advancePR(t *testing.T, bareDir, newContent string) {
	t.Helper()
	advanceDir := filepath.Join(t.TempDir(), "advance")
	runGit(t, t.TempDir(), "clone", bareDir, advanceDir)
	runGit(t, advanceDir, "fetch", "origin", "refs/pull/1/head:refs/pull/1/head")
	runGit(t, advanceDir, "checkout", "refs/pull/1/head")
	require.NoError(t, os.WriteFile(filepath.Join(advanceDir, "pr.txt"), []byte(newContent), 0o644))
	runGit(t, advanceDir, "add", "pr.txt")
	runGit(t, advanceDir, "commit", "-m", "advance PR")
	runGit(t, advanceDir, "push", bareDir, "HEAD:refs/pull/1/head")
}

func TestGitHubPR_RealGit_AppliesPinnedContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logger := zap.NewNop()

	bareDir, baseSHA, prSHA := setupBareRepoWithPR(t, "pr-content")
	require.Len(t, prSHA, _testSHALen)
	workerDir := cloneWorker(t, bareDir, baseSHA)

	req, err := workspace.NewRequest(changeURI("1", prSHA), git.New(workerDir, logger), bareDir, baseSHA, logger)
	require.NoError(t, err)
	require.NoError(t, req.Apply(ctx))

	content, err := os.ReadFile(filepath.Join(workerDir, "pr.txt"))
	require.NoError(t, err)
	assert.Equal(t, "pr-content", string(content))
}

func TestGitHubPR_RealGit_StableTreeAcrossPRAdvance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logger := zap.NewNop()

	bareDir, baseSHA, prSHA1 := setupBareRepoWithPR(t, "version-1")
	require.Len(t, prSHA1, _testSHALen)
	uri := changeURI("1", prSHA1)

	worker1 := cloneWorker(t, bareDir, baseSHA)
	g1 := git.New(worker1, logger)
	req1, err := workspace.NewRequest(uri, g1, bareDir, baseSHA, logger)
	require.NoError(t, err)
	require.NoError(t, req1.Apply(ctx))
	tree1, err := g1.RevParse(ctx, "HEAD^{tree}")
	require.NoError(t, err)

	advancePR(t, bareDir, "version-2")

	worker2 := cloneWorker(t, bareDir, baseSHA)
	g2 := git.New(worker2, logger)
	req2, err := workspace.NewRequest(uri, g2, bareDir, baseSHA, logger)
	require.NoError(t, err)
	require.NoError(t, req2.Apply(ctx))
	tree2, err := g2.RevParse(ctx, "HEAD^{tree}")
	require.NoError(t, err)

	assert.Equal(t, tree1, tree2, "the pinned head SHA must yield the same tree regardless of PR head advancement")

	content, err := os.ReadFile(filepath.Join(worker2, "pr.txt"))
	require.NoError(t, err)
	assert.Equal(t, "version-1", string(content), "the applied content must be the pinned version, not the advanced head")
}

func TestGitHubPR_RealGit_RejectsNonAncestorSHA(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logger := zap.NewNop()

	bareDir, baseSHA, _ := setupBareRepoWithPR(t, "original")

	sideDir := filepath.Join(t.TempDir(), "side")
	runGit(t, t.TempDir(), "clone", bareDir, sideDir)
	runGit(t, sideDir, "checkout", "-b", "side", baseSHA)
	require.NoError(t, os.WriteFile(filepath.Join(sideDir, "other.txt"), []byte("different"), 0o644))
	runGit(t, sideDir, "add", "other.txt")
	runGit(t, sideDir, "commit", "-m", "side commit")
	sideSHA := runGit(t, sideDir, "rev-parse", "HEAD")
	runGit(t, sideDir, "push", bareDir, "HEAD:refs/heads/side")

	workerDir := cloneWorker(t, bareDir, baseSHA)
	req, err := workspace.NewRequest(changeURI("1", sideSHA), git.New(workerDir, logger), bareDir, baseSHA, logger)
	require.NoError(t, err)
	require.Error(t, req.Apply(ctx))
}
