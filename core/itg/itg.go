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

// Package itg implements an incremental target graph provider for tango.
// It avoids full bazel queries by finding the nearest cached graph and
// applying incremental updates only to changed packages.
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
	"strings"

	"github.com/uber/tango/core/bazel"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/itg/cache"
	"github.com/uber/tango/core/itg/changeanalyzer"
	"github.com/uber/tango/core/itg/graph"
	"github.com/uber/tango/core/itg/workspaceutils"
	"github.com/uber/tango/core/targethasher"
	pb "github.com/uber/tango/tangopb"
)

// Config contains the configuration for the ITG provider.
type Config struct {
	// WorkspaceRoot is the absolute path to the bazel workspace root.
	WorkspaceRoot string
	// BuildFilePatterns are regexps that match build file paths (e.g., "BUILD", "BUILD.bazel").
	BuildFilePatterns []string
	// CriticalFilePatterns are regexps for files that require full recalculation (e.g., "WORKSPACE").
	CriticalFilePatterns []string
	// IgnoredFilePatterns are regexps for files whose changes are ignored (e.g., "METADATA").
	IgnoredFilePatterns []string
	// MinChangedFilesForGitHash is the threshold above which git ls-tree is used for file hashing.
	// Below this threshold, file hashes are computed from disk.
	MinChangedFilesForGitHash int
}

// HasherFactory creates a SourceHasher for the given workspace root and hash config.
type HasherFactory func(workspaceRoot string, knownHashes map[string][]byte, fullHashRepos []string, excludedFiles []string) targethasher.SourceHasher

// BazelFactory creates a Bazel client for the given workspace path.
// It is called per-request for workspace-specific bazel operations (step 2).
// The factory is responsible for injecting the correct bazel command and logger.
type BazelFactory func(workspacePath string) (bazel.Bazel, error)

// Provider implements IncrementalGraphProvider using the ITG algorithm.
type Provider struct {
	// git and bazel point to the origin clone and are used for step 1 only.
	// Step 1 checks out historical commits to compute the BaseSha graph.
	git          git.Interface
	bazel        bazel.Bazel
	cache        cache.Cache
	// analyzer is used for GetFileCategory (pattern matching only, no git).
	analyzer     changeanalyzer.Analyzer
	// analyzerCfg is stored so a per-request workspace analyzer can be created
	// in GetGraph for AnalyzeChange (which needs the workspace git for step 0).
	analyzerCfg  changeanalyzer.Config
	bazelFactory BazelFactory
	hasherFactory HasherFactory
	cfg           Config
}

// Params contains the dependencies for creating a new Provider.
type Params struct {
	// Git and Bazel point to the origin clone and are used for step 1.
	Git          git.Interface
	Bazel        bazel.Bazel
	// BazelFactory creates a per-request bazel client pointed at the workspace
	// path supplied in GetGraphRequest.WorkspacePath (step 2).
	BazelFactory BazelFactory
	Cache        cache.Cache
	HasherFactory HasherFactory
	Config        Config
}

var queryOptions = []string{"--order_output=no", "--proto:locations", "--noproto:default_values"}

// New creates a new ITG Provider.
func New(p Params) (*Provider, error) {
	analyzerCfg := changeanalyzer.Config{
		BuildFilePatterns:    p.Config.BuildFilePatterns,
		CriticalFilePatterns: p.Config.CriticalFilePatterns,
		IgnoredFilePatterns:  p.Config.IgnoredFilePatterns,
	}
	// Validate patterns upfront.
	a, err := changeanalyzer.NewAnalyzer(p.Git, analyzerCfg)
	if err != nil {
		return nil, fmt.Errorf("creating change analyzer: %w", err)
	}
	return &Provider{
		git:           p.Git,
		bazel:         p.Bazel,
		cache:         p.Cache,
		analyzer:      a,
		analyzerCfg:   analyzerCfg,
		bazelFactory:  p.BazelFactory,
		hasherFactory: p.HasherFactory,
		cfg:           p.Config,
	}, nil
}

// GetGraphRequest contains the parameters for GetGraph.
type GetGraphRequest struct {
	// BaseSha is a commit on main used to locate the nearest cached graph.
	BaseSha string
	// BaseShaTreeHash is the git tree hash at BaseSha, used to look up a cached graph.
	BaseShaTreeHash string
	// TargetRef is the ref to compute the graph for (diff is cacheKey.BaseSha..TargetRef).
	// This ref must be resolvable in the workspace at WorkspacePath.
	TargetRef string
	// Remote is the remote repo identifier used to namespace cache entries.
	Remote string
	// WorkspacePath is the absolute path to the worker workspace that has the user's
	// diffs applied (i.e. the workspace checked out to TargetRef). It is used for
	// step 2 git operations and the bazel query, so the ITG origin clone (p.git) is
	// not disturbed by user-diff checkouts.
	WorkspacePath string
}

// GetGraphResult contains the graphs produced by GetGraph.
type GetGraphResult struct {
	// TargetRefGraph is the graph computed at TargetRef (with user diffs applied).
	// The caller should write this to the main treehash cache keyed by TargetRef's treehash.
	TargetRefGraph []*pb.GetTargetGraphResponse
}

// GetGraph incrementally computes the full target graph for the given request.
// It uses a two-step approach:
//
//  1. zero_revision → BaseSha: computes the graph at BaseSha using the origin
//     clone (p.git / p.bazel), checking out historical commits there.
//  2. BaseSha → TargetRef: computes the incremental diff using the workspace
//     at req.WorkspacePath (already at TargetRef with user diffs applied),
//     so the origin clone state is not affected.
func (p *Provider) GetGraph(ctx context.Context, req GetGraphRequest) (GetGraphResult, error) {
	// Ensure BaseSha is present in the origin clone before any git operations.
	if _, err := p.git.RevParse(ctx, fmt.Sprintf("%s^{commit}", req.BaseSha)); err != nil {
		if fetchErr := p.git.Fetch(ctx, req.Remote, req.BaseSha); fetchErr != nil {
			return GetGraphResult{}, fmt.Errorf("fetching base sha %s: %w", req.BaseSha, fetchErr)
		}
	}

	baseShaCommitSecond, err := p.git.GetCommitTimeSecond(ctx, req.BaseSha)
	if err != nil {
		return GetGraphResult{}, fmt.Errorf("getting commit time for sha %s: %w", req.BaseSha, err)
	}

	// Build per-request workspace git and bazel for steps 0 and 2.
	// The workspace is already checked out to TargetRef (with user diffs applied),
	// so we must not do any checkouts there — only reads/diffs/bazel queries.
	wsGit := git.New(req.WorkspacePath)
	wsBazel, err := p.bazelFactory(req.WorkspacePath)
	if err != nil {
		return GetGraphResult{}, fmt.Errorf("creating workspace bazel client: %w", err)
	}
	// Per-request analyzer backed by the workspace git so AnalyzeChange sees the
	// correct "HEAD" (TargetRef with PR diffs) rather than the origin clone's HEAD.
	wsAnalyzer, err := changeanalyzer.NewAnalyzer(wsGit, p.analyzerCfg)
	if err != nil {
		return GetGraphResult{}, fmt.Errorf("creating workspace change analyzer: %w", err)
	}

	cacheKey, err := p.searchCachedGraph(ctx, req.Remote, baseShaCommitSecond)
	if err != nil {
		return GetGraphResult{}, fmt.Errorf("searching for cached graph: %w", err)
	}

	// Validate the full range (zero_revision → targetRef) upfront so we fail fast
	// before doing any checkout or bazel work.
	cacheToTarget, err := wsAnalyzer.AnalyzeChange(ctx, &changeanalyzer.AnalyzeChangeRequest{
		BaseRef:   cacheKey.BaseSha,
		TargetRef: req.TargetRef,
	})
	if err != nil {
		return GetGraphResult{}, fmt.Errorf("analyzing changes from cache to target ref: %w", err)
	}
	if !isSupportedChangeComplexity(cacheToTarget.ChangeComplexity) {
		return GetGraphResult{}, fmt.Errorf("unsupported change complexity from cache %s to target ref %s: %v", cacheKey.BaseSha, req.TargetRef, cacheToTarget.ChangeComplexity)
	}

	cacheGraph, err := p.cache.Get(ctx, cacheKey)
	if err != nil {
		return GetGraphResult{}, fmt.Errorf("loading cached graph: %w", err)
	}

	// Restore origin clone HEAD when done — step 1 checks out BaseSha there.
	curRef, err := p.git.RevParse(ctx, "HEAD")
	if err != nil {
		return GetGraphResult{}, fmt.Errorf("parsing HEAD: %w", err)
	}
	defer func() { _ = p.git.Checkout(ctx, curRef, "--force") }()

	// Step 1: zero_revision → BaseSha.
	// Uses the origin clone (p.git / p.bazel) so that workspace is not touched.
	baseShaKey := cache.Key{
		Remote:               req.Remote,
		BaseCommitTimeSecond: baseShaCommitSecond,
		BaseSha:              req.BaseSha,
	}
	baseShaGraph, err := p.calculateGraphIncrementally(ctx, calcParams{
		baseGraph:     cacheGraph,
		baseRef:       cacheKey.BaseSha,
		targetRef:     req.BaseSha,
		git:           p.git,
		bazel:         p.bazel,
		workspaceRoot: p.cfg.WorkspaceRoot,
	})
	if err != nil {
		return GetGraphResult{}, fmt.Errorf("incremental graph calculation to base sha: %w", err)
	}

	// Fast path: no user diffs applied — baseSha IS the target. Upload to the ITG cache
	// and return directly without a second incremental calculation or a Copy.
	if req.BaseSha == req.TargetRef {
		go func() {
			_ = p.cache.Put(ctx, baseShaGraph, baseShaKey)
		}()
		return GetGraphResult{
			TargetRefGraph: optimizedGraphToProto(baseShaGraph),
		}, nil
	}

	// Write baseShaGraph to the ITG cache in a goroutine so it runs concurrently
	// with the second calculateGraphIncrementally below (which mutates baseShaGraph
	// in-place). We clone first to avoid a data race with the gob encoder.
	// Cache write failures are non-fatal.
	baseShaGraphCopy := baseShaGraph.Copy()
	go func() {
		_ = p.cache.Put(ctx, baseShaGraphCopy, baseShaKey)
	}()

	// Step 2: BaseSha → TargetRef (user's diffs applied on top).
	// Uses the workspace git and bazel so the origin clone is not disturbed.
	// The workspace is already at TargetRef, so Checkout is a no-op there.
	targetGraph, err := p.calculateGraphIncrementally(ctx, calcParams{
		baseGraph:     baseShaGraph,
		baseRef:       req.BaseSha,
		targetRef:     req.TargetRef,
		git:           wsGit,
		bazel:         wsBazel,
		workspaceRoot: req.WorkspacePath,
	})
	if err != nil {
		return GetGraphResult{}, fmt.Errorf("incremental graph calculation to target ref: %w", err)
	}

	return GetGraphResult{
		TargetRefGraph: optimizedGraphToProto(targetGraph),
	}, nil
}

// SeedCacheRequest contains the parameters for SeedCache.
type SeedCacheRequest struct {
	// BaseSha is the main branch commit whose graph is being seeded.
	BaseSha string
	// BaseShaTreeHash is the git tree hash at BaseSha.
	BaseShaTreeHash string
	// Remote is the remote repo identifier used to namespace cache entries.
	Remote string
	// Graph is the full target graph result from a bazel query at BaseSha.
	Graph targethasher.Result
}

// SeedCache stores a full bazel query result in the ITG cache so future incremental
// requests can use it as a base. It should only be called when the query was run at
// a pure main-branch commit (no user diffs applied), i.e. HEAD == BaseSha.
func (p *Provider) SeedCache(ctx context.Context, req SeedCacheRequest) error {
	if _, err := p.git.RevParse(ctx, fmt.Sprintf("%s^{commit}", req.BaseSha)); err != nil {
		if fetchErr := p.git.Fetch(ctx, req.Remote, req.BaseSha); fetchErr != nil {
			return fmt.Errorf("fetching base sha %s: %w", req.BaseSha, fetchErr)
		}
	}

	commitTimeSecond, err := p.git.GetCommitTimeSecond(ctx, req.BaseSha)
	if err != nil {
		return fmt.Errorf("getting commit time for sha %s: %w", req.BaseSha, err)
	}

	optimizedGraph := graph.OptimizeGraph(req.Graph.Targets)

	key := cache.Key{
		Remote:               req.Remote,
		BaseCommitTimeSecond: commitTimeSecond,
		BaseSha:              req.BaseSha,
	}
	return p.cache.Put(ctx, optimizedGraph, key)
}

type calcParams struct {
	baseGraph     *graph.OptimizedGraph
	baseRef       string
	targetRef     string
	git           git.Interface
	bazel         bazel.Bazel
	workspaceRoot string
}

func (p *Provider) calculateGraphIncrementally(ctx context.Context, params calcParams) (*graph.OptimizedGraph, error) {
	if err := params.git.Checkout(ctx, params.targetRef); err != nil {
		return nil, err
	}

	rawChanges, err := params.git.DiffWithStatus(ctx, params.baseRef, params.targetRef)
	if err != nil {
		return nil, err
	}
	if len(rawChanges) == 0 {
		return params.baseGraph, nil
	}

	var knownHashes map[string][]byte
	if len(rawChanges) >= p.cfg.MinChangedFilesForGitHash {
		knownHashes, err = params.git.FileHashes(ctx, params.targetRef)
		if err != nil {
			return nil, err
		}
	}

	updateInput, err := p.parseChanges(rawChanges, graph.NewStringSet(), params.workspaceRoot)
	if err != nil {
		return nil, err
	}

	toQuery := getPackagesToRun(updateInput)
	if len(toQuery) == 0 {
		return params.baseGraph, nil
	}

	resp, err := params.bazel.ExecuteQuery(ctx, &bazel.QueryRequest{
		Query:          strings.Join(toQuery, " + "),
		AdditionalArgs: queryOptions,
	})
	if err != nil {
		return nil, fmt.Errorf("running bazel query: %w", err)
	}
	if resp.Result == nil {
		return params.baseGraph, nil
	}

	updateInput.QueryResult = resp.Result
	hasher := p.hasherFactory(params.workspaceRoot, knownHashes, nil, nil)
	if err := params.baseGraph.UpdateGraph(ctx, hasher, updateInput); err != nil {
		return nil, err
	}

	return params.baseGraph, nil
}

func (p *Provider) searchCachedGraph(ctx context.Context, remote string, baseRefCommitSecond int64) (cache.Key, error) {
	k, err := p.cache.FloorKey(ctx, remote, baseRefCommitSecond)
	if err != nil {
		return cache.EmptyKey, err
	}
	if k == cache.EmptyKey {
		return k, fmt.Errorf("cache key is empty")
	}
	return k, nil
}

func (p *Provider) parseChanges(changes []git.DiffEntry, excludedFiles graph.StringSet, workspaceRoot string) (graph.UpdateGraphInput, error) {
	deletedSrcFiles := graph.NewStringSet()
	changedPkgs := graph.NewStringSet()
	deletedPkgs := graph.NewStringSet()

	for _, changeInfo := range changes {
		fileCategory := p.analyzer.GetFileCategory(changeInfo.Path)
		status := changeanalyzer.ChangedFileStatus(changeInfo.Status)
		switch fileCategory {
		case changeanalyzer.BuildFile:
			curPkg := filepath.Dir(changeInfo.Path)
			if curPkg == "." {
				curPkg = ""
			}

			if status == changeanalyzer.Added || status == changeanalyzer.Modified {
				changedPkgs.Insert(curPkg)
			} else {
				deletedPkgs.Insert(curPkg)
			}

			shouldAddParent, err := p.shouldAddParentPkg(status, curPkg, workspaceRoot)
			if err != nil {
				return graph.UpdateGraphInput{}, fmt.Errorf("determine if should handle parent package: %w", err)
			}
			if shouldAddParent {
				parentPkg, err := workspaceutils.GetContainingPackage(workspaceRoot, curPkg)
				if !errors.Is(err, workspaceutils.ErrParentPackageNotExist) {
					if err != nil {
						return graph.UpdateGraphInput{}, fmt.Errorf("getting parent package: %w", err)
					}
					changedPkgs.Insert(parentPkg)
				}
			}
		case changeanalyzer.RegularFile:
			if status == changeanalyzer.Modified && isExcludedFile(changeInfo.Path, excludedFiles) {
				break
			}
			parentPkg, err := workspaceutils.GetContainingPackage(workspaceRoot, changeInfo.Path)
			if !errors.Is(err, workspaceutils.ErrParentPackageNotExist) {
				if err != nil {
					return graph.UpdateGraphInput{}, fmt.Errorf("getting parent package: %w", err)
				}
				changedPkgs.Insert(parentPkg)
			}
			if status == changeanalyzer.Deleted {
				deletedSrcFiles.Insert(changeInfo.Path)
			}
		}
	}

	return graph.UpdateGraphInput{
		DeletedSrcFiles: deletedSrcFiles,
		ChangedPkgs:     changedPkgs,
		DeletedPkgs:     deletedPkgs,
		WorkspaceRoot:   workspaceRoot,
		FullHashRepos:   graph.NewStringSet(),
	}, nil
}

func (p *Provider) shouldAddParentPkg(status changeanalyzer.ChangedFileStatus, curPkg string, workspaceRoot string) (bool, error) {
	if status == changeanalyzer.Added {
		return true, nil
	}
	if status == changeanalyzer.Deleted {
		_, err := os.Stat(filepath.Join(workspaceRoot, curPkg))
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	}
	return false, nil
}

func isSupportedChangeComplexity(complexity changeanalyzer.ChangeComplexity) bool {
	return complexity == changeanalyzer.NoChange ||
		complexity == changeanalyzer.RegularFilesModificationOnly ||
		complexity == changeanalyzer.ReparsePackagesNeeded
}

func isExcludedFile(file string, excludedFiles graph.StringSet) bool {
	for excludedFile := range excludedFiles {
		if match, err := regexp.MatchString(excludedFile, file); match || err != nil {
			return match
		}
	}
	return false
}

func getPackagesToRun(input graph.UpdateGraphInput) []string {
	toQuery := make([]string, 0, len(input.ChangedPkgs))
	for pkg := range input.ChangedPkgs {
		if input.DeletedPkgs.Contains(pkg) {
			continue
		}
		toQuery = append(toQuery, workspaceutils.PkgNameToTargetName(pkg))
	}
	return toQuery
}

// optimizedGraphToProto converts an OptimizedGraph to the proto response format.
func optimizedGraphToProto(g *graph.OptimizedGraph) []*pb.GetTargetGraphResponse {
	const chunkSize = 1000

	optimizedTargets := make([]*pb.OptimizedTarget, 0, len(g.OptimizedTargets))
	for id, t := range g.OptimizedTargets {
		depIDs := make([]int32, 0, len(t.Deps))
		for depID := range t.Deps {
			depIDs = append(depIDs, int32(depID))
		}

		tagIDs := make([]int32, len(t.Tags))
		for i, tagID := range t.Tags {
			tagIDs[i] = int32(tagID)
		}

		var attrs map[int32]int32
		if len(t.Attributes) > 0 {
			attrs = make(map[int32]int32, len(t.Attributes))
			for nameID, valID := range t.Attributes {
				attrs[int32(nameID)] = int32(valID)
			}
		}

		ot := &pb.OptimizedTarget{
			Id:                 int32(id),
			Hash:               hex.EncodeToString(t.Hash),
			DirectDependencies: depIDs,
			RuleType:           int32(t.RuleType),
			Tags:               tagIDs,
			Root:               t.Root,
			External:           t.External,
			Attributes:         attrs,
		}
		optimizedTargets = append(optimizedTargets, ot)
	}

	responses := chunkOptimizedTargets(optimizedTargets, chunkSize)

	targetIDToName := make(map[int32]string, len(g.TargetIDToString))
	for id, name := range g.TargetIDToString {
		targetIDToName[int32(id)] = name
	}
	ruleTypeIDToName := make(map[int32]string, len(g.RuleTypeIDToString))
	for id, name := range g.RuleTypeIDToString {
		ruleTypeIDToName[int32(id)] = name
	}
	tagIDToName := make(map[int32]string, len(g.TagIDToString))
	for id, name := range g.TagIDToString {
		tagIDToName[int32(id)] = name
	}
	attrNameIDToName := make(map[int32]string, len(g.AttrNameIDToString))
	for id, name := range g.AttrNameIDToString {
		attrNameIDToName[int32(id)] = name
	}
	attrValIDToVal := make(map[int32]string, len(g.AttrValueIDToString))
	for id, val := range g.AttrValueIDToString {
		attrValIDToVal[int32(id)] = val
	}

	responses = append(responses, &pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Metadata{
			Metadata: &pb.Metadata{
				TargetIdMapping:             targetIDToName,
				RuleTypeMapping:             ruleTypeIDToName,
				TagMapping:                  tagIDToName,
				AttributeNameMapping:        attrNameIDToName,
				AttributeStringValueMapping: attrValIDToVal,
			},
		},
	})

	return responses
}

func chunkOptimizedTargets(targets []*pb.OptimizedTarget, chunkSize int) []*pb.GetTargetGraphResponse {
	numChunks := max(1, (len(targets)+chunkSize-1)/chunkSize)
	responses := make([]*pb.GetTargetGraphResponse, 0, numChunks)
	for i := 0; i < len(targets); i += chunkSize {
		end := i + chunkSize
		if end > len(targets) {
			end = len(targets)
		}
		responses = append(responses, &pb.GetTargetGraphResponse{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{Targets: targets[i:end]},
			},
		})
	}
	if len(responses) == 0 {
		responses = append(responses, &pb.GetTargetGraphResponse{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{Targets: []*pb.OptimizedTarget{}},
			},
		})
	}
	return responses
}

// NewStringSet creates a StringSet from values (convenience re-export for users).
func NewStringSet(vals ...string) graph.StringSet {
	return graph.NewStringSet(vals...)
}

// diskHasherFactory creates a SourceHasher that reads from disk with optional known hashes.
// This is a convenience implementation; callers can provide their own HasherFactory.
func diskHasherFactory(workspaceRoot string, knownHashes map[string][]byte, fullHashRepos []string, excludedFiles []string) targethasher.SourceHasher {
	return targethasher.NewSourceHasher(targethasher.Params{
		WorkspaceRoot: workspaceRoot,
		HashConfig: targethasher.HashConfig{
			KnownSourceHashes: knownHashes,
			FullHashRepos:     fullHashRepos,
			ExcludedFiles:     excludedFiles,
		},
	})
}

// DefaultHasherFactory is a HasherFactory backed by disk and git file hashes.
var DefaultHasherFactory HasherFactory = diskHasherFactory
