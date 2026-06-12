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
	"strings"
	"sync"

	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/workspace"
	"github.com/uber/tango/tangopb"
	"go.uber.org/zap"
)

// RepoManager manages repository workspaces with a pool of workers per repo.
type RepoManager interface {
	Lease(ctx context.Context, desc tangopb.BuildDescription) (workspace.Workspace, error)
}

type repoManager struct {
	git                  git.Interface
	repoManagerClonePath string
	workerRootPath       string
	logger               *zap.SugaredLogger
	poolSize             int

	mu    sync.Mutex
	pools map[string]*workerPool

	// appCtx represents the app's overall lifetime. It is passed in by the
	// caller at construction and is expected to be cancelled when the whole
	// application is shutting down (e.g. on SIGTERM/SIGINT). Any future
	// fire-and-forget goroutines this manager starts should use this context
	// instead of context.Background() so they abort promptly on shutdown
	// rather than running unbounded past server teardown.
	//
	// Per-request cancellation should still use the request's own context;
	// appCtx is only for work that intentionally outlives the request.
	appCtx context.Context
}

// workerPool manages a fixed set of worker slots for a single repo.
// The origin directory holds the initial clone; workers are cheap local copies.
type workerPool struct {
	originDir string
	originMu  sync.Mutex // one lock per repo for orginal clone
	cloned    bool

	avail chan *workerSlot // available slots; pool capacity
}

// workerSlot is a pre-allocated workspace directory that may or may not
// have been cloned yet. Lazy creation on first use.
type workerSlot struct {
	dir     string
	created bool
}

// Params for creating a RepoManager.
type Params struct {
	Git                  git.Interface
	Logger               *zap.SugaredLogger
	RepoManagerClonePath string
	WorkerRootPath       string
	PoolSize             int
}

// NewRepoManager creates a new repo manager with pooled worker workspaces.
//
// appCtx is the application-lifetime context. Cancel it when the process is
// shutting down (e.g. wire it to SIGTERM/SIGINT in main) to abort any
// background goroutines the manager spawns.
func NewRepoManager(appCtx context.Context, p Params) RepoManager {
	return &repoManager{
		git:                  p.Git,
		repoManagerClonePath: p.RepoManagerClonePath,
		workerRootPath:       p.WorkerRootPath,
		logger:               p.Logger,
		poolSize:             p.PoolSize,
		pools:                make(map[string]*workerPool),
		appCtx:               appCtx,
	}
}

func (r *repoManager) poolFor(repo string) *workerPool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if pool, ok := r.pools[repo]; ok {
		return pool
	}
	// default to 1 worker workspace per repo
	if r.poolSize <= 0 {
		r.poolSize = 1
	}
	pool := &workerPool{
		originDir: filepath.Join(r.repoManagerClonePath, repo),
		avail:     make(chan *workerSlot, r.poolSize),
	}

	// Pre-allocate fixed worker slots. Existing directories from a previous
	// run are detected and reused without re-cloning.
	workersDir := filepath.Join(r.workerRootPath, repo)
	for i := 1; i <= r.poolSize; i++ {
		dir := filepath.Join(workersDir, fmt.Sprintf("worker-%d", i))
		slot := &workerSlot{dir: dir}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			slot.created = true
			r.logger.Debugf("discovered existing worker: %s", dir)
		}
		pool.avail <- slot
	}

	r.pools[repo] = pool
	return pool
}

// Lease borrows a worker workspace from the pool.
// If all workers are leased, it blocks until one is returned or ctx is cancelled.
func (r *repoManager) Lease(ctx context.Context, desc tangopb.BuildDescription) (workspace.Workspace, error) {
	repo := toShortRemote(desc.Remote)
	pool := r.poolFor(repo)

	if err := pool.ensureOrigin(ctx, r.git, desc.Remote); err != nil {
		return nil, err
	}

	// Acquire a worker slot (blocks if all slots are leased)
	var slot *workerSlot
	select {
	case slot = <-pool.avail:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Lazily create the worker clone on first use
	if !slot.created {
		if err := r.createWorker(ctx, pool.originDir, slot.dir); err != nil {
			pool.avail <- slot // return slot so others can retry
			return nil, fmt.Errorf("create worker: %w", err)
		}
		slot.created = true
	}

	repoGit := git.New(slot.dir)
	return workspace.NewWorkspace(workspace.WorkspaceParams{
		Path:   slot.dir,
		Git:    repoGit,
		Logger: r.logger,
		OnRelease: func() {
			pool.avail <- slot
		},
	}), nil
}

// ensureOrigin clones the origin repository if it doesn't exist yet.
func (p *workerPool) ensureOrigin(ctx context.Context, g git.Interface, remote string) error {
	p.originMu.Lock()
	defer p.originMu.Unlock()

	if p.cloned {
		return nil
	}
	if _, err := os.Stat(filepath.Join(p.originDir, ".git")); err == nil {
		p.cloned = true
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(p.originDir), 0o755); err != nil {
		return fmt.Errorf("mkdir origin dir: %w", err)
	}
	if err := g.Clone(ctx, remote, p.originDir, "-c", "gc.auto=0"); err != nil {
		os.RemoveAll(p.originDir)
		return fmt.Errorf("clone origin: %w", err)
	}
	p.cloned = true
	return nil
}

// createWorker creates a worker by cloning the origin with --local
// (fast and space-efficient).
func (r *repoManager) createWorker(ctx context.Context, originDir, workerDir string) error {
	os.RemoveAll(workerDir) // clean up any partial/corrupted previous state
	if err := os.MkdirAll(filepath.Dir(workerDir), 0o755); err != nil {
		return err
	}
	return r.git.Clone(ctx, originDir, workerDir, "--local", "-c", "gc.auto=0")
}

func toShortRemote(remote string) string {
	strs := strings.Split(remote, ":")
	return strs[len(strs)-1]
}
