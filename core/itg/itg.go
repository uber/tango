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

// Package itg implements the incremental target graph provider. It avoids a
// full `bazel query deps(//...:all-targets)` by loading the nearest cached
// graph at or before the base commit and re-querying only the packages that
// changed since then.
//
// The workspace is already checked out to the revision being computed, so the
// provider never checks anything out.
package itg

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/uber/tango/config"
	"github.com/uber/tango/core/bazel"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/itg/cache"
	"github.com/uber/tango/core/itg/changeanalyzer"
	"github.com/uber/tango/core/itg/graph"
	"github.com/uber/tango/core/itg/workspaceutils"
	"github.com/uber/tango/core/targethasher"
)

// File-classification patterns. A .bzl file is classified by the analyzer as
// an extension file, which is an unsupported complexity: a .bzl edit can
// affect every package that loads it, so it always falls back to a full query.
var (
	// _buildFilePatterns match files whose package must be re-queried.
	_buildFilePatterns = []string{`(^|/)BUILD(\.bazel)?$`}

	// _criticalFilePatterns match files that invalidate the whole graph.
	_criticalFilePatterns = []string{
		`^WORKSPACE$`,
		`^WORKSPACE\.bzlmod$`,
		`^MODULE\.bazel(\.lock)?$`,
		`^\.bazelrc$`,
		`(^|/)bazelrc$`,
		`^go\.mod$`,
		`^go\.sum$`,
		`^nogo\.json$`,
	}
)

// _baseQueryArgs are the bazel query flags shared with the full-query path in
// graphrunner.nativeGraphRunner.Compute. A flag that changes the shape of the
// query proto changes the resulting target hashes, so they must stay in sync.
var _baseQueryArgs = []string{"--order_output=no", "--proto:locations", "--noproto:default_values"}

// Provider computes target graphs incrementally.
type Provider interface {
	// GetGraph returns the graph of the checked-out workspace, built by
	// applying the changes since the nearest cached graph at or before
	// req.BaseSha. It returns an error, never a partial graph, when the change
	// cannot be applied incrementally, so callers can fall back to a full query.
	GetGraph(ctx context.Context, req GetGraphRequest) (*graph.OptimizedGraph, error)

	// SeedCache stores a full-query result so later runs have a base to build
	// on. Without it the cache stays cold and GetGraph never succeeds.
	SeedCache(ctx context.Context, req SeedCacheRequest) error
}

// GetGraphRequest contains the parameters for GetGraph.
type GetGraphRequest struct {
	// BaseSha is a commit on the default branch used to locate the nearest
	// cached graph.
	BaseSha string
	// Remote is the git remote BaseSha is fetched from when the workspace does
	// not have it.
	Remote string
}

// SeedCacheRequest contains the parameters for SeedCache.
type SeedCacheRequest struct {
	// BaseSha is the default-branch commit whose graph is being seeded.
	BaseSha string
	// Remote is the git remote BaseSha is fetched from when the workspace does
	// not have it.
	Remote string
	// Result is the full-query result of BaseSha with no change requests and
	// no per-request exclusions. Must not be mutated while SeedCache runs.
	Result targethasher.Result
}

// Params contains the dependencies for creating a Provider.
type Params struct {
	// Git operates on the workspace at WorkspacePath.
	Git git.Interface
	// Bazel queries the workspace at WorkspacePath.
	Bazel bazel.Bazel
	// Cache stores optimized graphs.
	Cache cache.Cache
	// CacheNamespace scopes cache entries to one repository.
	CacheNamespace string
	// WorkspacePath is the absolute path to the bazel workspace root.
	WorkspacePath string
	// RepositoryConfig supplies the bazel and hashing settings that must match
	// the full-query path so an incremental graph is comparable to a full one.
	RepositoryConfig config.RepositoryConfig
}

type provider struct {
	git           git.Interface
	bazel         bazel.Bazel
	cache         cache.Cache
	namespace     string
	analyzer      changeanalyzer.Analyzer
	workspacePath string
	cfg           config.RepositoryConfig
	excludedRx    []*regexp.Regexp
	useBzlmod     bool
}

var _ Provider = (*provider)(nil)

// New creates a Provider.
func New(p Params) (Provider, error) {
	switch {
	case p.WorkspacePath == "":
		return nil, errors.New("workspace path is required")
	case p.Git == nil:
		return nil, errors.New("git client is required")
	case p.Bazel == nil:
		return nil, errors.New("bazel client is required")
	case p.Cache == nil:
		return nil, errors.New("cache is required")
	}

	analyzer, err := changeanalyzer.NewAnalyzer(p.Git, changeanalyzer.Config{
		BuildFilePatterns:    _buildFilePatterns,
		CriticalFilePatterns: _criticalFilePatterns,
	})
	if err != nil {
		return nil, fmt.Errorf("creating change analyzer: %w", err)
	}

	excludedRx := make([]*regexp.Regexp, 0, len(p.RepositoryConfig.ExcludedFiles))
	for _, pattern := range p.RepositoryConfig.ExcludedFiles {
		rx, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("compiling excluded file pattern %q: %w", pattern, err)
		}
		excludedRx = append(excludedRx, rx)
	}

	return &provider{
		git:           p.Git,
		bazel:         p.Bazel,
		cache:         p.Cache,
		namespace:     p.CacheNamespace,
		analyzer:      analyzer,
		workspacePath: p.WorkspacePath,
		cfg:           p.RepositoryConfig,
		excludedRx:    excludedRx,
		useBzlmod:     p.RepositoryConfig.BzlmodEnabled == nil || *p.RepositoryConfig.BzlmodEnabled,
	}, nil
}

// OptimizedGraphToResult converts g to a targethasher.Result so it can go
// through the same mapper.ResultToGraphChunks and storage pipeline as a
// full-query result.
func OptimizedGraphToResult(g *graph.OptimizedGraph) targethasher.Result {
	names := make([]string, 0, len(g.OptimizedTargets))
	targets := make(map[string]*targethasher.Target, len(g.OptimizedTargets))
	for id := range g.OptimizedTargets {
		t := g.OptimizedTargetToTarget(id)
		for _, attr := range t.Attributes {
			tp := buildpb.Attribute_STRING
			attr.Type = &tp
		}
		targets[t.Name] = &t
		names = append(names, t.Name)
	}
	sortNamesByLabel(names)
	return targethasher.Result{
		Targets:              targets,
		TargetNames:          names,
		AllTargetsFileHashes: g.AllTargetsFileHashes,
	}
}

// GetGraph incrementally computes the full target graph of the workspace.
func (p *provider) GetGraph(ctx context.Context, req GetGraphRequest) (*graph.OptimizedGraph, error) {
	baseCommitSecond, err := p.commitTimeSecond(ctx, req.Remote, req.BaseSha)
	if err != nil {
		return nil, err
	}

	cacheKey, err := p.cache.FloorKey(ctx, p.namespace, baseCommitSecond)
	if err != nil {
		return nil, fmt.Errorf("searching for cached graph: %w", err)
	}
	if cacheKey == cache.EmptyKey {
		return nil, errors.New("no cached graph at or before the base commit")
	}

	// Diff from the cached commit to HEAD. The workspace is already at base
	// plus any change requests, so one range covers the gap to the base
	// commit and the change requests together. Validate it before downloading
	// the cached graph so unsupported changes bail out cheaply.
	analysis, err := p.analyzer.AnalyzeChange(ctx, &changeanalyzer.AnalyzeChangeRequest{
		BaseRef:   cacheKey.BaseSha,
		TargetRef: "HEAD",
	})
	if err != nil {
		return nil, fmt.Errorf("analyzing changes from %s to HEAD: %w", cacheKey.BaseSha, err)
	}
	if !isSupportedChangeComplexity(analysis.ChangeComplexity) {
		return nil, fmt.Errorf("unsupported change complexity from cache %s to HEAD: %v",
			cacheKey.BaseSha, analysis.ChangeComplexity)
	}

	// Load the cached graph concurrently with the bazel query: it is the most
	// expensive step and is independent of the query.
	cacheCtx, cancelCache := context.WithCancel(ctx)
	defer cancelCache() // stops the load if we return before consuming it
	type cached struct {
		graph *graph.OptimizedGraph
		err   error
	}
	cacheCh := make(chan cached, 1)
	go func() {
		g, err := p.cache.Get(cacheCtx, cacheKey)
		cacheCh <- cached{graph: g, err: err}
	}()
	awaitCache := func() (*graph.OptimizedGraph, error) {
		c := <-cacheCh
		if c.err != nil {
			return nil, fmt.Errorf("loading cached graph: %w", c.err)
		}
		return c.graph, nil
	}

	// Resolve git blob hashes the same way the full-query path does; otherwise
	// the same source file hashes differently depending on the path taken.
	knownHashes, err := p.git.FileHashes(ctx, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("reading file hashes at HEAD: %w", err)
	}

	input, err := p.parseChanges(analysis.Changes)
	if err != nil {
		return nil, err
	}
	if input.ChangedPkgs.Contains("") {
		return nil, errors.New("root package changed, falling back to full query")
	}

	toQuery := packagesToQuery(input)
	if len(toQuery) == 0 {
		g, err := awaitCache()
		if err != nil {
			return nil, err
		}
		p.populateAllTargetsFileHashes(g, knownHashes)
		return g, nil
	}

	resp, err := p.bazel.ExecuteQuery(ctx, &bazel.QueryRequest{
		Query:          strings.Join(toQuery, " + "),
		StartupOptions: p.cfg.BazelStartupOptions,
		AdditionalArgs: slices.Concat(_baseQueryArgs, p.cfg.BazelExtraArgs),
	})
	if err == nil && resp.Result == nil {
		err = errors.New("nil query result")
	}
	if err != nil {
		return nil, fmt.Errorf("running bazel query over %d packages: %w", len(toQuery), err)
	}

	g, err := awaitCache()
	if err != nil {
		return nil, err
	}

	input.QueryResult = resp.Result
	hasher := targethasher.NewSourceHasher(targethasher.Params{
		WorkspaceRoot: p.workspacePath,
		HashConfig: targethasher.HashConfig{
			KnownSourceHashes: knownHashes,
			FullHashRepos:     p.cfg.FullHashRepos,
			ExcludedRegex:     p.cfg.ExcludedFiles,
			UseBzlmod:         p.useBzlmod,
		},
	})
	if err := g.UpdateGraph(ctx, hasher, input); err != nil {
		return nil, fmt.Errorf("updating graph: %w", err)
	}
	p.populateAllTargetsFileHashes(g, knownHashes)
	return g, nil
}

// SeedCache stores req.Result as the base graph for req.BaseSha. The cache
// skips the write when the commit is already cached.
func (p *provider) SeedCache(ctx context.Context, req SeedCacheRequest) error {
	if len(req.Result.Targets) == 0 {
		return errors.New("SeedCache requires a non-empty Result")
	}
	commitSecond, err := p.commitTimeSecond(ctx, req.Remote, req.BaseSha)
	if err != nil {
		return err
	}

	optimized := graph.OptimizeGraph(req.Result.Targets)
	optimized.AllTargetsFileHashes = req.Result.AllTargetsFileHashes
	return p.cache.Put(ctx, optimized, cache.Key{
		Remote:               p.namespace,
		BaseCommitTimeSecond: commitSecond,
		BaseSha:              req.BaseSha,
	})
}

// commitTimeSecond resolves the commit timestamp of sha, fetching it first if
// the workspace does not already have the commit.
func (p *provider) commitTimeSecond(ctx context.Context, remote, sha string) (int64, error) {
	if _, err := p.git.RevParse(ctx, sha+"^{commit}"); err != nil {
		if fetchErr := p.git.Fetch(ctx, remote, sha); fetchErr != nil {
			return 0, fmt.Errorf("fetching base sha %s: %w", sha, fetchErr)
		}
	}
	second, err := p.git.GetCommitTimeSecond(ctx, sha)
	if err != nil {
		return 0, fmt.Errorf("getting commit time for sha %s: %w", sha, err)
	}
	return second, nil
}

// populateAllTargetsFileHashes sets AllTargetsFileHashes on g the way the full
// query path does: the hex git blob hash of each configured file, or "" when
// the file is absent. Without it, comparing an incremental graph against a
// full one reports every target as changed.
func (p *provider) populateAllTargetsFileHashes(g *graph.OptimizedGraph, knownHashes map[string][]byte) {
	if len(p.cfg.AllTargetsFiles) == 0 {
		return
	}
	hashes := make(map[string]string, len(p.cfg.AllTargetsFiles))
	for _, f := range p.cfg.AllTargetsFiles {
		hashes[f] = hex.EncodeToString(knownHashes[f])
	}
	g.AllTargetsFileHashes = hashes
}

// parseChanges maps each changed file to the package that must be re-queried.
func (p *provider) parseChanges(changes []git.DiffEntry) (graph.UpdateGraphInput, error) {
	deletedSrcFiles := graph.NewStringSet()
	changedPkgs := graph.NewStringSet()
	deletedPkgs := graph.NewStringSet()

	for _, change := range changes {
		status := changeanalyzer.ChangedFileStatus(change.Status)
		switch p.analyzer.GetFileCategory(change.Path) {
		case changeanalyzer.BuildFile:
			pkg := filepath.Dir(change.Path)
			if pkg == "." {
				pkg = ""
			}

			if status == changeanalyzer.Added || status == changeanalyzer.Modified {
				changedPkgs.Insert(pkg)
			} else {
				deletedPkgs.Insert(pkg)
			}

			shouldAddParent, err := p.shouldAddParentPkg(status, pkg)
			if err != nil {
				return graph.UpdateGraphInput{}, fmt.Errorf("determine if should handle parent package: %w", err)
			}
			if !shouldAddParent {
				continue
			}
			parentPkg, err := workspaceutils.GetContainingPackage(p.workspacePath, pkg)
			if errors.Is(err, workspaceutils.ErrParentPackageNotExist) {
				continue
			}
			if err != nil {
				return graph.UpdateGraphInput{}, fmt.Errorf("getting parent package: %w", err)
			}
			changedPkgs.Insert(parentPkg)

		case changeanalyzer.RegularFile:
			if status == changeanalyzer.Modified && strings.Contains(change.Path, "/") &&
				p.isExcludedFile(workspaceutils.RelFilePathToTargetName(change.Path)) {
				continue
			}
			parentPkg, err := workspaceutils.GetContainingPackage(p.workspacePath, change.Path)
			if err != nil && !errors.Is(err, workspaceutils.ErrParentPackageNotExist) {
				return graph.UpdateGraphInput{}, fmt.Errorf("getting parent package: %w", err)
			}
			if err == nil {
				changedPkgs.Insert(parentPkg)
			}
			if status == changeanalyzer.Deleted && strings.Contains(change.Path, "/") {
				deletedSrcFiles.Insert(workspaceutils.RelFilePathToTargetName(change.Path))
			}

		case changeanalyzer.IgnoredFile:
			if status == changeanalyzer.Deleted {
				if parentPkg, err := workspaceutils.GetContainingPackage(p.workspacePath, change.Path); err == nil {
					changedPkgs.Insert(parentPkg)
				}
			}
		}
	}

	return graph.UpdateGraphInput{
		DeletedSrcFiles: deletedSrcFiles,
		ChangedPkgs:     changedPkgs,
		DeletedPkgs:     deletedPkgs,
		WorkspaceRoot:   p.workspacePath,
		FullHashRepos:   graph.NewStringSet(p.cfg.FullHashRepos...),
		UseBzlmod:       p.useBzlmod,
		ExcludedRegex:   p.excludedRx,
	}, nil
}

// shouldAddParentPkg reports whether the package containing pkg also needs
// re-querying: adding a BUILD file carves targets out of the parent package,
// and deleting one folds them back in, but only if the directory still exists.
func (p *provider) shouldAddParentPkg(status changeanalyzer.ChangedFileStatus, pkg string) (bool, error) {
	switch status {
	case changeanalyzer.Added:
		return true, nil
	case changeanalyzer.Deleted:
		_, err := os.Stat(filepath.Join(p.workspacePath, pkg))
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	default:
		return false, nil
	}
}

func (p *provider) isExcludedFile(file string) bool {
	for _, rx := range p.excludedRx {
		if rx.MatchString(file) {
			return true
		}
	}
	return false
}

func isSupportedChangeComplexity(complexity changeanalyzer.ChangeComplexity) bool {
	return complexity == changeanalyzer.NoChange ||
		complexity == changeanalyzer.RegularFilesModificationOnly ||
		complexity == changeanalyzer.ReparsePackagesNeeded
}

// packagesToQuery returns the bazel target patterns for every changed package
// that still exists.
func packagesToQuery(input graph.UpdateGraphInput) []string {
	toQuery := make([]string, 0, len(input.ChangedPkgs))
	for pkg := range input.ChangedPkgs {
		if input.DeletedPkgs.Contains(pkg) {
			continue
		}
		toQuery = append(toQuery, workspaceutils.PkgNameToTargetName(pkg))
	}
	sort.Strings(toQuery)
	return toQuery
}

// sortNamesByLabel sorts names by (package, name), matching the order the TGB
// encoder uses.
func sortNamesByLabel(names []string) {
	split := func(label string) (string, string) {
		if i := strings.LastIndex(label, ":"); i >= 0 {
			return label[:i], label[i+1:]
		}
		return "", label
	}
	sort.Slice(names, func(i, j int) bool {
		pi, ni := split(names[i])
		pj, nj := split(names[j])
		if pi != pj {
			return pi < pj
		}
		return ni < nj
	})
}
