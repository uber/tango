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

package repomanager

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/config"
	tangogit "github.com/uber/tango/core/git"
	gitmock "github.com/uber/tango/core/git/gitmock"
	"github.com/uber/tango/core/workspace"
	"github.com/uber/tango/entity"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
)

type requestFunc func(context.Context) error

type testRepositoryConfigProvider struct{}

func (testRepositoryConfigProvider) GetRepositoryConfig(remote string) (config.RepositoryConfig, bool) {
	return config.RepositoryConfig{
		Remote:       remote,
		RepositoryID: testRepositoryID(remote),
	}, true
}

func (f requestFunc) Apply(ctx context.Context) error {
	return f(ctx)
}

func newTestRepoManager(t *testing.T, appCtx context.Context, p Params) RepoManager {
	t.Helper()
	if p.RepoConfig == nil {
		p.RepoConfig = testRepositoryConfigProvider{}
	}
	rm, err := NewRepoManager(appCtx, p)
	require.NoError(t, err)
	rm.(*repoManager).restoreWorker = func(context.Context, string) error { return nil }
	return rm
}

func runRepoGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, output)
}

func testOriginDir(root, remote string) string {
	return filepath.Join(root, testRepositoryID(remote))
}

func testWorkerDir(root, remote string, worker int) string {
	return filepath.Join(root, ".workers", testRepositoryID(remote), fmt.Sprintf("worker-%d", worker))
}

func testRepositoryID(remote string) string {
	return fmt.Sprintf("repository-%x", sha256.Sum256([]byte(remote)))
}

func TestNewRepoManager_InvalidPoolSize(t *testing.T) {
	t.Parallel()
	_, err := NewRepoManager(context.Background(), Params{
		Logger:   zap.NewNop(),
		PoolSize: 0,
	})
	require.Error(t, err, "expected setting invalid poolsize to error out")
}

func TestLease_ClonesOriginAndCreatesWorker(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	originDir := testOriginDir(root, remote)
	workerDir := testWorkerDir(root, remote, 1)

	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(nil)

	rm := newTestRepoManager(t, context.Background(), Params{Git: g, Logger: zap.NewNop(), RepoManagerClonePath: root, PoolSize: 1})
	ws, err := rm.Lease(context.Background(), entity.BuildDescription{Remote: remote})
	require.NoError(t, err)
	assert.Equal(t, workerDir, ws.Path())
	require.NoError(t, ws.Release())
}

func TestLease_SkipsOriginClone_WhenExists(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	originDir := testOriginDir(root, remote)
	workerDir := testWorkerDir(root, remote, 1)

	require.NoError(t, os.MkdirAll(filepath.Join(originDir, ".git"), 0o755))

	// Only worker clone expected
	g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(nil)

	rm := newTestRepoManager(t, context.Background(), Params{Git: g, Logger: zap.NewNop(), RepoManagerClonePath: root, PoolSize: 1})
	ws, err := rm.Lease(context.Background(), entity.BuildDescription{Remote: remote})
	require.NoError(t, err)
	assert.Equal(t, workerDir, ws.Path())
	require.NoError(t, ws.Release())
}

func TestLease_ReusesWorker_AfterRelease(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	originDir := testOriginDir(root, remote)
	workerDir := testWorkerDir(root, remote, 1)

	// Exactly 1 origin + 1 worker clone total
	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(nil)

	rm := newTestRepoManager(t, context.Background(), Params{Git: g, Logger: zap.NewNop(), RepoManagerClonePath: root, PoolSize: 1})
	ctx := context.Background()

	ws1, err := rm.Lease(ctx, entity.BuildDescription{Remote: remote})
	require.NoError(t, err)
	require.NoError(t, ws1.Release())

	// Second lease reuses the same worker — no new clones
	ws2, err := rm.Lease(ctx, entity.BuildDescription{Remote: remote})
	require.NoError(t, err)
	assert.Equal(t, workerDir, ws2.Path())
	require.NoError(t, ws2.Release())
}

func TestLease_RestoresWorkerAfterFailedMaterialization(t *testing.T) {
	sourceDir := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	runRepoGit(t, sourceDir, "init")
	runRepoGit(t, sourceDir, "config", "user.email", "test@example.com")
	runRepoGit(t, sourceDir, "config", "user.name", "Test User")
	runRepoGit(t, sourceDir, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, ".gitignore"), []byte("generated.txt\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "tracked.txt"), []byte("original\n"), 0o644))
	runRepoGit(t, sourceDir, "add", ".gitignore", "tracked.txt")
	runRepoGit(t, sourceDir, "commit", "-m", "initial")

	root := t.TempDir()
	rm, err := NewRepoManager(context.Background(), Params{
		Git:                  tangogit.New(t.TempDir(), zap.NewNop()),
		Logger:               zap.NewNop(),
		RepoManagerClonePath: root,
		PoolSize:             1,
		RepoConfig:           testRepositoryConfigProvider{},
	})
	require.NoError(t, err)

	ws1, err := rm.Lease(context.Background(), entity.BuildDescription{Remote: sourceDir})
	require.NoError(t, err)
	workerDir := ws1.Path()
	reuseMarker := filepath.Join(workerDir, ".git", "reuse-marker")
	require.NoError(t, os.WriteFile(reuseMarker, []byte("keep"), 0o644))

	materializeErr := ws1.ApplyRequests(context.Background(), []workspace.Request{
		requestFunc(func(context.Context) error {
			require.NoError(t, os.WriteFile(filepath.Join(workerDir, "tracked.txt"), []byte("dirty\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(workerDir, "untracked.txt"), []byte("untracked\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(workerDir, "generated.txt"), []byte("generated\n"), 0o644))
			runRepoGit(t, workerDir, "add", "tracked.txt")
			return assert.AnError
		}),
	})
	require.ErrorIs(t, materializeErr, assert.AnError)
	require.NoError(t, ws1.Release())

	ws2, err := rm.Lease(context.Background(), entity.BuildDescription{Remote: sourceDir})
	require.NoError(t, err)
	assert.Equal(t, workerDir, ws2.Path())
	assert.FileExists(t, reuseMarker, "a successfully restored worker should be reused rather than recloned")

	content, err := os.ReadFile(filepath.Join(workerDir, "tracked.txt"))
	require.NoError(t, err)
	assert.Equal(t, "original\n", string(content))
	assert.NoFileExists(t, filepath.Join(workerDir, "untracked.txt"))
	assert.NoFileExists(t, filepath.Join(workerDir, "generated.txt"))

	cmd := exec.Command("git", "status", "--porcelain", "--untracked-files=all")
	cmd.Dir = workerDir
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Empty(t, output)
	require.NoError(t, ws2.Release())
}

func TestLease_RecreatesWorker_WhenRestoreFails(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	originDir := testOriginDir(root, remote)
	workerDir := testWorkerDir(root, remote, 1)

	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(nil).Times(2)

	rm := newTestRepoManager(t, context.Background(), Params{Git: g, Logger: zap.NewNop(), RepoManagerClonePath: root, PoolSize: 1})
	manager := rm.(*repoManager)
	manager.restoreWorker = func(context.Context, string) error { return assert.AnError }

	ws1, err := rm.Lease(context.Background(), entity.BuildDescription{Remote: remote})
	require.NoError(t, err)
	require.NoError(t, ws1.Release())

	ws2, err := rm.Lease(context.Background(), entity.BuildDescription{Remote: remote})
	require.NoError(t, err)
	assert.Equal(t, workerDir, ws2.Path())
	require.NoError(t, ws2.Release())
}

func TestLease_CreatesMultipleWorkers(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	originDir := testOriginDir(root, remote)

	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	for i := 1; i <= 2; i++ {
		dir := testWorkerDir(root, remote, i)
		g.EXPECT().Clone(gomock.Any(), originDir, dir, "--local", "-c", "gc.auto=0").Return(nil)
	}

	rm := newTestRepoManager(t, context.Background(), Params{Git: g, Logger: zap.NewNop(), RepoManagerClonePath: root, PoolSize: 2})
	ctx := context.Background()

	ws1, err := rm.Lease(ctx, entity.BuildDescription{Remote: remote})
	require.NoError(t, err)
	ws2, err := rm.Lease(ctx, entity.BuildDescription{Remote: remote})
	require.NoError(t, err)

	assert.NotEqual(t, ws1.Path(), ws2.Path())
	require.NoError(t, ws1.Release())
	require.NoError(t, ws2.Release())
}

func TestLease_BlocksUntilReturn(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	originDir := testOriginDir(root, remote)
	workerDir := testWorkerDir(root, remote, 1)

	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(nil)

	rm := newTestRepoManager(t, context.Background(), Params{Git: g, Logger: zap.NewNop(), RepoManagerClonePath: root, PoolSize: 1})
	ctx := context.Background()

	ws1, err := rm.Lease(ctx, entity.BuildDescription{Remote: remote})
	require.NoError(t, err)

	// Second lease blocks because pool size = 1
	done := make(chan error, 1)
	go func() {
		ws2, err := rm.Lease(ctx, entity.BuildDescription{Remote: remote})
		if err == nil {
			ws2.Release()
		}
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("second lease should block")
	default:
	}

	require.NoError(t, ws1.Release())

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("second lease did not unblock")
	}
}

func TestLease_CtxCanceled(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	originDir := testOriginDir(root, remote)
	workerDir := testWorkerDir(root, remote, 1)

	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(nil)

	rm := newTestRepoManager(t, context.Background(), Params{Git: g, Logger: zap.NewNop(), RepoManagerClonePath: root, PoolSize: 1})

	ws1, err := rm.Lease(context.Background(), entity.BuildDescription{Remote: remote})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = rm.Lease(ctx, entity.BuildDescription{Remote: remote})
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrPoolTimeout), "cancelled context should not produce ErrPoolTimeout")
	assert.True(t, errors.Is(err, context.Canceled), "expected underlying context.Canceled")

	require.NoError(t, ws1.Release())
}

func TestLease_CtxDeadlineExceeded(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	originDir := testOriginDir(root, remote)
	workerDir := testWorkerDir(root, remote, 1)

	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(nil)

	rm := newTestRepoManager(t, context.Background(), Params{Git: g, Logger: zap.NewNop(), RepoManagerClonePath: root, PoolSize: 1})

	ws1, err := rm.Lease(context.Background(), entity.BuildDescription{Remote: remote})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	_, err = rm.Lease(ctx, entity.BuildDescription{Remote: remote})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPoolTimeout), "deadline exceeded should produce ErrPoolTimeout")
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "expected underlying context.DeadlineExceeded")

	require.NoError(t, ws1.Release())
}

func TestLease_OriginCloneFails(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	g.EXPECT().Clone(gomock.Any(), remote, testOriginDir(root, remote), "-c", "gc.auto=0").Return(assert.AnError)

	rm := newTestRepoManager(t, context.Background(), Params{Git: g, Logger: zap.NewNop(), RepoManagerClonePath: root, PoolSize: 1})
	_, err := rm.Lease(context.Background(), entity.BuildDescription{Remote: remote})
	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError)
}

func TestLease_WorkerCloneFails(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	originDir := testOriginDir(root, remote)
	workerDir := testWorkerDir(root, remote, 1)

	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(assert.AnError)

	rm := newTestRepoManager(t, context.Background(), Params{Git: g, Logger: zap.NewNop(), RepoManagerClonePath: root, PoolSize: 1})
	_, err := rm.Lease(context.Background(), entity.BuildDescription{Remote: remote})
	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError)
}

func TestLease_DiscoversExistingWorker(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	originDir := testOriginDir(root, remote)
	workerDir := testWorkerDir(root, remote, 1)

	// Pre-create origin and worker from a "previous run"
	require.NoError(t, os.MkdirAll(filepath.Join(originDir, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workerDir, ".git"), 0o755))

	// No Clone calls — everything already exists
	rm := newTestRepoManager(t, context.Background(), Params{Git: g, Logger: zap.NewNop(), RepoManagerClonePath: root, PoolSize: 1})
	ws, err := rm.Lease(context.Background(), entity.BuildDescription{Remote: remote})
	require.NoError(t, err)
	assert.Contains(t, ws.Path(), "worker-1")
	require.NoError(t, ws.Release())
}

func TestLease_DifferentRepos_IndependentPools(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote1 := "git@github.com:org/repo"
	remote2 := "git@gitlab.com:org/repo"

	origin1 := testOriginDir(root, remote1)
	origin2 := testOriginDir(root, remote2)
	worker1 := testWorkerDir(root, remote1, 1)
	worker2 := testWorkerDir(root, remote2, 1)

	g.EXPECT().Clone(gomock.Any(), remote1, origin1, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), origin1, worker1, "--local", "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), remote2, origin2, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), origin2, worker2, "--local", "-c", "gc.auto=0").Return(nil)

	rm := newTestRepoManager(t, context.Background(), Params{Git: g, Logger: zap.NewNop(), RepoManagerClonePath: root, PoolSize: 1})
	ctx := context.Background()

	// Both repos can be leased concurrently even with pool size 1
	ws1, err := rm.Lease(ctx, entity.BuildDescription{Remote: remote1})
	require.NoError(t, err)
	ws2, err := rm.Lease(ctx, entity.BuildDescription{Remote: remote2})
	require.NoError(t, err)

	assert.Equal(t, worker1, ws1.Path())
	assert.Equal(t, worker2, ws2.Path())
	assert.NotEqual(t, ws1.Path(), ws2.Path())

	require.NoError(t, ws1.Release())
	require.NoError(t, ws2.Release())
}

func TestLease_WorkerCloneFails_SlotReturnedToPool(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	originDir := testOriginDir(root, remote)
	workerDir := testWorkerDir(root, remote, 1)

	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	// First attempt fails, second succeeds
	gomock.InOrder(
		g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(assert.AnError),
		g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(nil),
	)

	rm := newTestRepoManager(t, context.Background(), Params{Git: g, Logger: zap.NewNop(), RepoManagerClonePath: root, PoolSize: 1})
	ctx := context.Background()

	// First attempt fails
	_, err := rm.Lease(ctx, entity.BuildDescription{Remote: remote})
	require.Error(t, err)

	// Slot was returned to pool — retry succeeds
	ws, err := rm.Lease(ctx, entity.BuildDescription{Remote: remote})
	require.NoError(t, err)
	require.NoError(t, ws.Release())
}
