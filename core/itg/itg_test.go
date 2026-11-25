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

package itg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/config"
	"github.com/uber/tango/core/bazel"
	"github.com/uber/tango/core/bazel/bazelmock"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/git/gitmock"
	"github.com/uber/tango/core/itg/cache"
	"github.com/uber/tango/core/itg/graph"
	"github.com/uber/tango/core/targethasher"
	"go.uber.org/mock/gomock"
)

// fakeCache is a hand-rolled cache.Cache; the cache package ships no mock.
type fakeCache struct {
	floorKey    cache.Key
	floorKeyErr error
	getGraph    *graph.OptimizedGraph
	getErr      error
	putKey      cache.Key
	putGraph    *graph.OptimizedGraph
	putErr      error
}

func (f *fakeCache) FloorKey(context.Context, string, int64) (cache.Key, error) {
	return f.floorKey, f.floorKeyErr
}

func (f *fakeCache) Get(context.Context, cache.Key) (*graph.OptimizedGraph, error) {
	return f.getGraph, f.getErr
}

func (f *fakeCache) Put(_ context.Context, g *graph.OptimizedGraph, key cache.Key) error {
	f.putGraph, f.putKey = g, key
	return f.putErr
}

type harness struct {
	provider *provider
	git      *gitmock.MockInterface
	bazel    *bazelmock.MockBazel
	cache    *fakeCache
	root     string
}

// newHarness builds a Provider over mocks and a temp workspace whose root is a
// bazel package. The cache holds a graph for "cachedsha"; the base commit
// "basesha" is already present in the workspace.
func newHarness(t *testing.T, cfg config.RepositoryConfig) *harness {
	t.Helper()
	ctrl := gomock.NewController(t)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "BUILD.bazel"), nil, 0o644))

	h := &harness{
		git:   gitmock.NewMockInterface(ctrl),
		bazel: bazelmock.NewMockBazel(ctrl),
		cache: &fakeCache{
			floorKey: cache.Key{Remote: "repo", BaseCommitTimeSecond: 900, BaseSha: "cachedsha"},
			getGraph: graph.OptimizeGraph(map[string]*targethasher.Target{}),
		},
		root: root,
	}
	p, err := New(Params{
		Git:              h.git,
		Bazel:            h.bazel,
		Cache:            h.cache,
		CacheNamespace:   "repo",
		WorkspacePath:    root,
		RepositoryConfig: cfg,
	})
	require.NoError(t, err)
	impl, ok := p.(*provider)
	require.True(t, ok)
	h.provider = impl

	h.git.EXPECT().RevParse(gomock.Any(), "basesha^{commit}").Return("basesha", nil).AnyTimes()
	h.git.EXPECT().GetCommitTimeSecond(gomock.Any(), "basesha").Return(int64(1000), nil).AnyTimes()
	return h
}

func (h *harness) getGraph() (*graph.OptimizedGraph, error) {
	return h.provider.getGraph(context.Background(), GetResultRequest{BaseSha: "basesha", Remote: "remote"})
}

func (h *harness) expectDiff(entries ...git.DiffEntry) {
	h.git.EXPECT().DiffWithStatus(gomock.Any(), "cachedsha", "HEAD").Return(entries, nil)
}

func TestNew(t *testing.T) {
	ctrl := gomock.NewController(t)
	valid := Params{
		Git:           gitmock.NewMockInterface(ctrl),
		Bazel:         bazelmock.NewMockBazel(ctrl),
		Cache:         &fakeCache{},
		WorkspacePath: t.TempDir(),
	}
	tests := []struct {
		name    string
		mutate  func(*Params)
		wantErr bool
	}{
		{name: "valid", mutate: func(*Params) {}},
		{name: "missing workspace", mutate: func(p *Params) { p.WorkspacePath = "" }, wantErr: true},
		{name: "missing git", mutate: func(p *Params) { p.Git = nil }, wantErr: true},
		{name: "missing bazel", mutate: func(p *Params) { p.Bazel = nil }, wantErr: true},
		{name: "missing cache", mutate: func(p *Params) { p.Cache = nil }, wantErr: true},
		{
			name: "invalid excluded regex",
			mutate: func(p *Params) {
				p.RepositoryConfig = config.RepositoryConfig{ExcludedFiles: []string{"("}}
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := valid
			tt.mutate(&p)
			_, err := New(p)
			assert.Equal(t, tt.wantErr, err != nil)
		})
	}
}

func TestGetGraph_Fallbacks(t *testing.T) {
	tests := []struct {
		name  string
		setup func(h *harness)
	}{
		{
			name:  "cold cache",
			setup: func(h *harness) { h.cache.floorKey = cache.EmptyKey },
		},
		{
			name:  "floor key error",
			setup: func(h *harness) { h.cache.floorKeyErr = errors.New("boom") },
		},
		{
			name:  "critical file changed",
			setup: func(h *harness) { h.expectDiff(git.DiffEntry{Status: "M", Path: "MODULE.bazel"}) },
		},
		{
			// A .bzl edit can affect every package that loads it.
			name:  "extension file changed",
			setup: func(h *harness) { h.expectDiff(git.DiffEntry{Status: "M", Path: "rules/defs.bzl"}) },
		},
		{
			name: "root package changed",
			setup: func(h *harness) {
				h.expectDiff(git.DiffEntry{Status: "M", Path: "BUILD.bazel"})
				h.git.EXPECT().FileHashes(gomock.Any(), "HEAD").Return(nil, nil)
			},
		},
		{
			name: "cached graph unreadable",
			setup: func(h *harness) {
				h.cache.getErr = errors.New("download failed")
				h.expectDiff()
				h.git.EXPECT().FileHashes(gomock.Any(), "HEAD").Return(nil, nil)
			},
		},
		{
			name: "file hashes error",
			setup: func(h *harness) {
				h.expectDiff()
				h.git.EXPECT().FileHashes(gomock.Any(), "HEAD").Return(nil, errors.New("boom"))
			},
		},
		{
			name: "bazel query error",
			setup: func(h *harness) {
				addPackage(t, h.root, "svc")
				h.expectDiff(git.DiffEntry{Status: "M", Path: "svc/main.go"})
				h.git.EXPECT().FileHashes(gomock.Any(), "HEAD").Return(nil, nil)
				h.bazel.EXPECT().ExecuteQuery(gomock.Any(), gomock.Any()).Return(nil, errors.New("boom"))
			},
		},
		{
			name: "bazel query returns no result",
			setup: func(h *harness) {
				addPackage(t, h.root, "svc")
				h.expectDiff(git.DiffEntry{Status: "M", Path: "svc/main.go"})
				h.git.EXPECT().FileHashes(gomock.Any(), "HEAD").Return(nil, nil)
				h.bazel.EXPECT().ExecuteQuery(gomock.Any(), gomock.Any()).Return(&bazel.QueryResponse{}, nil)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, config.RepositoryConfig{})
			tt.setup(h)
			got, err := h.getGraph()
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

func TestGetGraph_FetchesMissingBaseSha(t *testing.T) {
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)
	g.EXPECT().RevParse(gomock.Any(), "basesha^{commit}").Return("", errors.New("unknown revision"))
	g.EXPECT().Fetch(gomock.Any(), "remote", "basesha").Return(nil)
	g.EXPECT().GetCommitTimeSecond(gomock.Any(), "basesha").Return(int64(1000), nil)

	p, err := New(Params{
		Git:           g,
		Bazel:         bazelmock.NewMockBazel(ctrl),
		Cache:         &fakeCache{}, // cold: only the fetch is under test
		WorkspacePath: t.TempDir(),
	})
	require.NoError(t, err)
	_, err = p.GetResult(context.Background(), GetResultRequest{BaseSha: "basesha", Remote: "remote"})
	require.Error(t, err)
}

func TestGetGraph_NoChangesReturnsCachedGraph(t *testing.T) {
	h := newHarness(t, config.RepositoryConfig{AllTargetsFiles: []string{"BUILD.bazel", "missing.txt"}})
	h.expectDiff()
	h.git.EXPECT().FileHashes(gomock.Any(), "HEAD").Return(map[string][]byte{"BUILD.bazel": {0xab, 0xcd}}, nil)

	got, err := h.getGraph()
	require.NoError(t, err)
	assert.Same(t, h.cache.getGraph, got)
	assert.Equal(t, map[string]string{"BUILD.bazel": "abcd", "missing.txt": ""}, got.AllTargetsFileHashes)
}

// The incremental query must carry the same bazel flags as the full-query
// path, or the graphs it produces are not comparable.
func TestGetGraph_QueriesChangedPackages(t *testing.T) {
	h := newHarness(t, config.RepositoryConfig{
		BazelStartupOptions: []string{"--bazelrc=custom"},
		BazelExtraArgs:      []string{"--repo_env=X=1"},
	})
	addPackage(t, h.root, "svc/foo")
	h.expectDiff(git.DiffEntry{Status: "M", Path: "svc/foo/main.go"})
	h.git.EXPECT().FileHashes(gomock.Any(), "HEAD").Return(nil, nil)

	var gotReq *bazel.QueryRequest
	h.bazel.EXPECT().ExecuteQuery(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req *bazel.QueryRequest) (*bazel.QueryResponse, error) {
			gotReq = req
			return &bazel.QueryResponse{Result: &buildpb.QueryResult{}}, nil
		})

	_, err := h.getGraph()
	require.NoError(t, err)
	require.NotNil(t, gotReq)
	assert.Equal(t, "//svc/foo:*", gotReq.Query)
	assert.Equal(t, []string{"--bazelrc=custom"}, gotReq.StartupOptions)
	assert.Equal(t, []string{
		"--order_output=no", "--proto:locations", "--noproto:default_values", "--repo_env=X=1",
	}, gotReq.AdditionalArgs)
}

func TestSeedCache(t *testing.T) {
	result := targethasher.Result{
		Targets:              map[string]*targethasher.Target{"//a:a": {Name: "//a:a", RuleType: "go_library"}},
		AllTargetsFileHashes: map[string]string{"f": "ab"},
	}

	t.Run("stores the graph under the base commit", func(t *testing.T) {
		h := newHarness(t, config.RepositoryConfig{})
		err := h.provider.SeedCache(context.Background(), SeedCacheRequest{BaseSha: "basesha", Remote: "remote", Result: result})
		require.NoError(t, err)
		assert.Equal(t, cache.Key{Remote: "repo", BaseCommitTimeSecond: 1000, BaseSha: "basesha"}, h.cache.putKey)
		require.NotNil(t, h.cache.putGraph)
		assert.Equal(t, result.AllTargetsFileHashes, h.cache.putGraph.AllTargetsFileHashes)
	})

	t.Run("errors", func(t *testing.T) {
		h := newHarness(t, config.RepositoryConfig{})
		h.cache.putErr = errors.New("boom")
		require.Error(t, h.provider.SeedCache(context.Background(), SeedCacheRequest{BaseSha: "basesha", Result: result}))
		require.Error(t, h.provider.SeedCache(context.Background(), SeedCacheRequest{BaseSha: "basesha"}))
	})
}

func TestParseChanges(t *testing.T) {
	root := t.TempDir()
	// The root and svc/foo are packages; svc/foo/nested is not.
	require.NoError(t, os.WriteFile(filepath.Join(root, "BUILD.bazel"), nil, 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "svc/foo/nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "svc/foo/BUILD.bazel"), nil, 0o644))

	tests := []struct {
		name        string
		excluded    []string
		changes     []git.DiffEntry
		wantChanged []string
		wantDeleted []string
		wantSrcs    []string
	}{
		{
			name:        "source file maps to its package",
			changes:     []git.DiffEntry{{Status: "M", Path: "svc/foo/main.go"}},
			wantChanged: []string{"svc/foo"},
		},
		{
			name:        "source file in a non-package dir maps to the nearest ancestor",
			changes:     []git.DiffEntry{{Status: "M", Path: "svc/foo/nested/util.go"}},
			wantChanged: []string{"svc/foo"},
		},
		{
			name:        "deleted source file is recorded for removal",
			changes:     []git.DiffEntry{{Status: "D", Path: "svc/foo/gone.go"}},
			wantChanged: []string{"svc/foo"},
			wantSrcs:    []string{"//svc/foo:gone.go"},
		},
		{
			name:        "added build file also re-queries the parent package",
			changes:     []git.DiffEntry{{Status: "A", Path: "svc/foo/nested/BUILD.bazel"}},
			wantChanged: []string{"svc/foo", "svc/foo/nested"},
		},
		{
			name:        "deleted build file whose directory is gone only marks the package deleted",
			changes:     []git.DiffEntry{{Status: "D", Path: "svc/removed/BUILD.bazel"}},
			wantDeleted: []string{"svc/removed"},
		},
		{
			name:        "deleted build file whose directory remains re-queries the parent",
			changes:     []git.DiffEntry{{Status: "D", Path: "svc/foo/nested/BUILD.bazel"}},
			wantChanged: []string{"svc/foo"},
			wantDeleted: []string{"svc/foo/nested"},
		},
		{
			name:     "modified excluded file is skipped",
			excluded: []string{`\.pb\.go$`},
			changes:  []git.DiffEntry{{Status: "M", Path: "svc/foo/x.pb.go"}},
		},
		{
			name:        "root build file",
			changes:     []git.DiffEntry{{Status: "M", Path: "BUILD.bazel"}},
			wantChanged: []string{""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			pr, err := New(Params{
				Git:              gitmock.NewMockInterface(ctrl),
				Bazel:            bazelmock.NewMockBazel(ctrl),
				Cache:            &fakeCache{},
				WorkspacePath:    root,
				RepositoryConfig: config.RepositoryConfig{ExcludedFiles: tt.excluded},
			})
			require.NoError(t, err)

			impl, ok := pr.(*provider)
			require.True(t, ok)
			got, err := impl.parseChanges(tt.changes)
			require.NoError(t, err)
			assert.Equal(t, sorted(tt.wantChanged), sorted(got.ChangedPkgs.UnsortedList()))
			assert.Equal(t, sorted(tt.wantDeleted), sorted(got.DeletedPkgs.UnsortedList()))
			assert.Equal(t, sorted(tt.wantSrcs), sorted(got.DeletedSrcFiles.UnsortedList()))
		})
	}
}

func TestPackagesToQuery_SkipsDeletedPackages(t *testing.T) {
	got := packagesToQuery(graph.UpdateGraphInput{
		ChangedPkgs: graph.NewStringSet("b", "a", "gone"),
		DeletedPkgs: graph.NewStringSet("gone"),
	})
	assert.Equal(t, []string{"//a:*", "//b:*"}, got)
}

func TestToResult(t *testing.T) {
	name, val, typ := "importpath", "example.com/foo", buildpb.Attribute_STRING
	g := graph.OptimizeGraph(map[string]*targethasher.Target{
		"//pkg:b": {Name: "//pkg:b", RuleType: "go_library", HashWithoutDeps: []byte("b"), Hash: []byte("b")},
		"//pkg:a": {
			Name:            "//pkg:a",
			RuleType:        "go_library",
			Deps:            []string{"//pkg:b"},
			HashWithoutDeps: []byte("a"),
			Hash:            []byte("a"),
			Attributes:      []*buildpb.Attribute{{Name: &name, StringValue: &val, Type: &typ}},
		},
	})
	g.AllTargetsFileHashes = map[string]string{"BUILD.bazel": "deadbeef"}

	result := toResult(g)

	assert.Equal(t, []string{"//pkg:a", "//pkg:b"}, result.TargetNames)
	assert.Equal(t, g.AllTargetsFileHashes, result.AllTargetsFileHashes)
	require.Len(t, result.Targets, 2)
	a := result.Targets["//pkg:a"]
	require.NotNil(t, a)
	assert.Equal(t, []string{"//pkg:b"}, a.Deps)
	require.Len(t, a.Attributes, 1)
	assert.Equal(t, buildpb.Attribute_STRING, a.Attributes[0].GetType())
}

func TestSortNamesByLabel(t *testing.T) {
	names := []string{"//b:a", "//a:z", "//a:b", "//a"}
	sortNamesByLabel(names)
	assert.Equal(t, []string{"//a", "//a:b", "//a:z", "//b:a"}, names)
}

func addPackage(t *testing.T, root, pkg string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, pkg), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, pkg, "BUILD.bazel"), nil, 0o644))
}

func sorted(s []string) []string {
	out := append([]string{}, s...)
	sort.Strings(out)
	return out
}

func TestGetResult(t *testing.T) {
	h := newHarness(t, config.RepositoryConfig{})
	h.cache.getGraph = graph.OptimizeGraph(map[string]*targethasher.Target{
		"//a:a": {Name: "//a:a", RuleType: "go_library", HashWithoutDeps: []byte("a"), Hash: []byte("a")},
	})
	h.expectDiff()
	h.git.EXPECT().FileHashes(gomock.Any(), "HEAD").Return(nil, nil)

	got, err := h.provider.GetResult(context.Background(), GetResultRequest{BaseSha: "basesha", Remote: "remote"})
	require.NoError(t, err)
	assert.Equal(t, []string{"//a:a"}, got.TargetNames)
}
