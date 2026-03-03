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
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitmock "github.com/uber/tango/core/git/gitmock"
	"github.com/uber/tango/tangopb"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
)

func TestLease_ClonesOriginAndCreatesWorker(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	originDir := filepath.Join(root, "org/repo")
	workerDir := filepath.Join(root, ".workers", "org/repo", "worker-1")

	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(nil)

	rm := NewRepoManager(Params{Git: g, Logger: zap.NewNop().Sugar(), RepoManagerClonePath: root, WorkerRootPath: filepath.Join(root, ".workers"), PoolSize: 1})
	ws, err := rm.Lease(context.Background(), tangopb.BuildDescription{Remote: remote})
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
	originDir := filepath.Join(root, "org/repo")
	workerDir := filepath.Join(root, ".workers", "org/repo", "worker-1")

	require.NoError(t, os.MkdirAll(filepath.Join(originDir, ".git"), 0o755))

	// Only worker clone expected
	g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(nil)

	rm := NewRepoManager(Params{Git: g, Logger: zap.NewNop().Sugar(), RepoManagerClonePath: root, WorkerRootPath: filepath.Join(root, ".workers"), PoolSize: 1})
	ws, err := rm.Lease(context.Background(), tangopb.BuildDescription{Remote: remote})
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
	originDir := filepath.Join(root, "org/repo")
	workerDir := filepath.Join(root, ".workers", "org/repo", "worker-1")

	// Exactly 1 origin + 1 worker clone total
	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(nil)

	rm := NewRepoManager(Params{Git: g, Logger: zap.NewNop().Sugar(), RepoManagerClonePath: root, WorkerRootPath: filepath.Join(root, ".workers"), PoolSize: 1})
	ctx := context.Background()

	ws1, err := rm.Lease(ctx, tangopb.BuildDescription{Remote: remote})
	require.NoError(t, err)
	require.NoError(t, ws1.Release())

	// Second lease reuses the same worker — no new clones
	ws2, err := rm.Lease(ctx, tangopb.BuildDescription{Remote: remote})
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
	originDir := filepath.Join(root, "org/repo")

	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	for i := 1; i <= 2; i++ {
		dir := filepath.Join(root, ".workers", "org/repo", fmt.Sprintf("worker-%d", i))
		g.EXPECT().Clone(gomock.Any(), originDir, dir, "--local", "-c", "gc.auto=0").Return(nil)
	}

	rm := NewRepoManager(Params{Git: g, Logger: zap.NewNop().Sugar(), RepoManagerClonePath: root, WorkerRootPath: filepath.Join(root, ".workers"), PoolSize: 2})
	ctx := context.Background()

	ws1, err := rm.Lease(ctx, tangopb.BuildDescription{Remote: remote})
	require.NoError(t, err)
	ws2, err := rm.Lease(ctx, tangopb.BuildDescription{Remote: remote})
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
	originDir := filepath.Join(root, "org/repo")
	workerDir := filepath.Join(root, ".workers", "org/repo", "worker-1")

	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(nil)

	rm := NewRepoManager(Params{Git: g, Logger: zap.NewNop().Sugar(), RepoManagerClonePath: root, WorkerRootPath: filepath.Join(root, ".workers"), PoolSize: 1})
	ctx := context.Background()

	ws1, err := rm.Lease(ctx, tangopb.BuildDescription{Remote: remote})
	require.NoError(t, err)

	// Second lease blocks because pool size = 1
	done := make(chan error, 1)
	go func() {
		ws2, err := rm.Lease(ctx, tangopb.BuildDescription{Remote: remote})
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
	originDir := filepath.Join(root, "org/repo")
	workerDir := filepath.Join(root, ".workers", "org/repo", "worker-1")

	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(nil)

	rm := NewRepoManager(Params{Git: g, Logger: zap.NewNop().Sugar(), RepoManagerClonePath: root, WorkerRootPath: filepath.Join(root, ".workers"), PoolSize: 1})

	ws1, err := rm.Lease(context.Background(), tangopb.BuildDescription{Remote: remote})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = rm.Lease(ctx, tangopb.BuildDescription{Remote: remote})
	require.Error(t, err)

	require.NoError(t, ws1.Release())
}

func TestLease_OriginCloneFails(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	g.EXPECT().Clone(gomock.Any(), remote, filepath.Join(root, "org/repo"), "-c", "gc.auto=0").Return(assert.AnError)

	rm := NewRepoManager(Params{Git: g, Logger: zap.NewNop().Sugar(), RepoManagerClonePath: root, WorkerRootPath: filepath.Join(root, ".workers"), PoolSize: 1})
	_, err := rm.Lease(context.Background(), tangopb.BuildDescription{Remote: remote})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clone origin")
}

func TestLease_WorkerCloneFails(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	originDir := filepath.Join(root, "org/repo")
	workerDir := filepath.Join(root, ".workers", "org/repo", "worker-1")

	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(assert.AnError)

	rm := NewRepoManager(Params{Git: g, Logger: zap.NewNop().Sugar(), RepoManagerClonePath: root, WorkerRootPath: filepath.Join(root, ".workers"), PoolSize: 1})
	_, err := rm.Lease(context.Background(), tangopb.BuildDescription{Remote: remote})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create worker")
}

func TestLease_DiscoversExistingWorker(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"

	// Pre-create origin and worker from a "previous run"
	require.NoError(t, os.MkdirAll(filepath.Join(root, "org/repo", ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".workers", "org/repo", "worker-1", ".git"), 0o755))

	// No Clone calls — everything already exists
	rm := NewRepoManager(Params{Git: g, Logger: zap.NewNop().Sugar(), RepoManagerClonePath: root, WorkerRootPath: filepath.Join(root, ".workers"), PoolSize: 1})
	ws, err := rm.Lease(context.Background(), tangopb.BuildDescription{Remote: remote})
	require.NoError(t, err)
	assert.Contains(t, ws.Path(), "worker-1")
	require.NoError(t, ws.Release())
}

func TestLease_DifferentRepos_IndependentPools(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote1 := "git@github.com:org/repo1"
	remote2 := "git@github.com:org/repo2"

	origin1 := filepath.Join(root, "org/repo1")
	origin2 := filepath.Join(root, "org/repo2")
	worker1 := filepath.Join(root, ".workers", "org/repo1", "worker-1")
	worker2 := filepath.Join(root, ".workers", "org/repo2", "worker-1")

	g.EXPECT().Clone(gomock.Any(), remote1, origin1, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), origin1, worker1, "--local", "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), remote2, origin2, "-c", "gc.auto=0").Return(nil)
	g.EXPECT().Clone(gomock.Any(), origin2, worker2, "--local", "-c", "gc.auto=0").Return(nil)

	rm := NewRepoManager(Params{Git: g, Logger: zap.NewNop().Sugar(), RepoManagerClonePath: root, WorkerRootPath: filepath.Join(root, ".workers"), PoolSize: 1})
	ctx := context.Background()

	// Both repos can be leased concurrently even with pool size 1
	ws1, err := rm.Lease(ctx, tangopb.BuildDescription{Remote: remote1})
	require.NoError(t, err)
	ws2, err := rm.Lease(ctx, tangopb.BuildDescription{Remote: remote2})
	require.NoError(t, err)

	assert.Contains(t, ws1.Path(), "repo1")
	assert.Contains(t, ws2.Path(), "repo2")

	require.NoError(t, ws1.Release())
	require.NoError(t, ws2.Release())
}

func TestLease_WorkerCloneFails_SlotReturnedToPool(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)

	root := t.TempDir()
	remote := "git@github.com:org/repo"
	originDir := filepath.Join(root, "org/repo")
	workerDir := filepath.Join(root, ".workers", "org/repo", "worker-1")

	g.EXPECT().Clone(gomock.Any(), remote, originDir, "-c", "gc.auto=0").Return(nil)
	// First attempt fails, second succeeds
	gomock.InOrder(
		g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(assert.AnError),
		g.EXPECT().Clone(gomock.Any(), originDir, workerDir, "--local", "-c", "gc.auto=0").Return(nil),
	)

	rm := NewRepoManager(Params{Git: g, Logger: zap.NewNop().Sugar(), RepoManagerClonePath: root, WorkerRootPath: filepath.Join(root, ".workers"), PoolSize: 1})
	ctx := context.Background()

	// First attempt fails
	_, err := rm.Lease(ctx, tangopb.BuildDescription{Remote: remote})
	require.Error(t, err)

	// Slot was returned to pool — retry succeeds
	ws, err := rm.Lease(ctx, tangopb.BuildDescription{Remote: remote})
	require.NoError(t, err)
	require.NoError(t, ws.Release())
}
