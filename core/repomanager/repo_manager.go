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

const defaultPoolSize = 3

// RepoManager manages repository workspaces with a pool of workers per repo.
type RepoManager interface {
	Lease(ctx context.Context, desc tangopb.BuildDescription) (workspace.Workspace, error)
}

type repoManager struct {
	git           git.Interface
	rootWorkspace string
	logger        *zap.SugaredLogger
	poolSize      int

	mu    sync.Mutex
	pools map[string]*workerPool
}

// workerPool manages a fixed set of worker slots for a single repo.
// The origin directory holds the initial clone; workers are cheap local copies.
type workerPool struct {
	originDir string
	originMu  sync.Mutex // one lock per repo for orginal clone
	cloned    bool

	avail chan *workerSlot // available slots; = pool capacity
}

// workerSlot is a pre-allocated workspace directory that may or may not
// have been cloned yet. Lazy creation on first use.
type workerSlot struct {
	dir     string
	created bool
}

// Params for creating a RepoManager.
type Params struct {
	Git           git.Interface
	Logger        *zap.SugaredLogger
	RootWorkspace string
	PoolSize      int // number of worker workspaces per repo; 0 uses default (3)
}

// NewRepoManager creates a new repo manager with pooled worker workspaces.
func NewRepoManager(p Params) RepoManager {
	size := p.PoolSize
	if size <= 0 {
		size = defaultPoolSize
	}
	return &repoManager{
		git:           p.Git,
		rootWorkspace: p.RootWorkspace,
		logger:        p.Logger,
		poolSize:      size,
		pools:         make(map[string]*workerPool),
	}
}

func (r *repoManager) poolFor(repo string) *workerPool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if pool, ok := r.pools[repo]; ok {
		return pool
	}

	pool := &workerPool{
		originDir: filepath.Join(r.rootWorkspace, repo),
		avail:     make(chan *workerSlot, r.poolSize),
	}

	// Pre-allocate fixed worker slots. Existing directories from a previous
	// run are detected and reused without re-cloning.
	workersDir := filepath.Join(r.rootWorkspace, ".workers", repo)
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
	ws := workspace.NewWorkspace(workspace.WorkspaceParams{
		Path:   slot.dir,
		Git:    repoGit,
		Logger: r.logger,
	})
	return &pooledWorkspace{Workspace: ws, pool: pool, slot: slot}, nil
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
	if err := g.Clone(ctx, remote, p.originDir); err != nil {
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
	return r.git.Clone(ctx, originDir, workerDir, "--local")
}

// pooledWorkspace wraps a workspace and returns its slot to the pool on release.
type pooledWorkspace struct {
	workspace.Workspace
	pool *workerPool
	slot *workerSlot
}

func (pw *pooledWorkspace) Release() error {
	pw.pool.avail <- pw.slot
	return nil
}

func toShortRemote(remote string) string {
	strs := strings.Split(remote, ":")
	return strs[len(strs)-1]
}
