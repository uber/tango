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

package graph

import (
	"context"
	"crypto/sha1"
	"regexp"
	"testing"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/targethasher"
	"pgregory.net/rapid"
)

// fakeSourceHasher is a test double for targethasher.SourceHasher.
type fakeSourceHasher struct {
	result []byte
	err    error
	ctx    context.Context
}

func (f *fakeSourceHasher) HashSourceFile(ctx context.Context, _ *buildpb.SourceFile) ([]byte, error) {
	f.ctx = ctx
	return f.result, f.err
}

// --- computeAvailableHashes ---

func TestComputeAvailableHashes(t *testing.T) {
	t.Parallel()

	t.Run("source file hash comes from hasher", func(t *testing.T) {
		t.Parallel()
		expected := []byte{0xAB, 0xCD}
		hasher := &fakeSourceHasher{result: expected}
		ctx := context.WithValue(context.Background(), struct{}{}, "source-hash")
		name := "//pkg:file.go"
		targets := map[string]*targethasher.Target{
			name: {
				Name:       name,
				RuleType:   targethasher.SourceFileType,
				SourceFile: &buildpb.SourceFile{},
			},
		}

		require.NoError(t, computeAvailableHashes(ctx, hasher, targets, nil))
		assert.Equal(t, expected, targets[name].Hash)
		assert.Equal(t, ctx, hasher.ctx)
	})

	t.Run("package group hashed by name", func(t *testing.T) {
		t.Parallel()
		name := "//pkg:__pkg__"
		targets := map[string]*targethasher.Target{
			name: {Name: name, RuleType: targethasher.PackageGroup},
		}

		require.NoError(t, computeAvailableHashes(context.Background(), &fakeSourceHasher{}, targets, nil))

		h := sha1.New()
		h.Write([]byte(name))
		assert.Equal(t, h.Sum(nil), targets[name].Hash)
	})

	t.Run("external rule type is skipped", func(t *testing.T) {
		t.Parallel()
		name := "//external:repo"
		targets := map[string]*targethasher.Target{
			name: {Name: name, RuleType: targethasher.ExternalRuleType},
		}

		require.NoError(t, computeAvailableHashes(context.Background(), &fakeSourceHasher{}, targets, nil))
		assert.Nil(t, targets[name].Hash, "external rule targets should not get a hash here")
	})

	t.Run("generated file gets no hash at this stage", func(t *testing.T) {
		t.Parallel()
		name := "//pkg:gen.go"
		targets := map[string]*targethasher.Target{
			name: {Name: name, RuleType: targethasher.GeneratedFileType},
		}

		require.NoError(t, computeAvailableHashes(context.Background(), &fakeSourceHasher{}, targets, nil))
		assert.Nil(t, targets[name].Hash, "generated file hash is resolved later")
	})

	t.Run("rule target gets HashWithoutDeps set", func(t *testing.T) {
		t.Parallel()
		name := "//pkg:lib"
		ruleName := name
		ruleClass := "go_library"
		targets := map[string]*targethasher.Target{
			name: {
				Name:     name,
				RuleType: "go_library",
				Rule:     &buildpb.Rule{Name: &ruleName, RuleClass: &ruleClass},
			},
		}

		require.NoError(t, computeAvailableHashes(context.Background(), &fakeSourceHasher{}, targets, nil))
		assert.NotNil(t, targets[name].HashWithoutDeps, "rule should have HashWithoutDeps after hashing")
		assert.Nil(t, targets[name].Hash, "full hash is not computed here — deps are needed")
	})

	t.Run("source hasher error is propagated", func(t *testing.T) {
		t.Parallel()
		hasher := &fakeSourceHasher{err: assert.AnError}
		targets := map[string]*targethasher.Target{
			"//pkg:f": {Name: "//pkg:f", RuleType: targethasher.SourceFileType, SourceFile: &buildpb.SourceFile{}},
		}

		err := computeAvailableHashes(context.Background(), hasher, targets, nil)
		assert.Error(t, err)
	})

	t.Run("excluded source file target is skipped without calling hasher", func(t *testing.T) {
		t.Parallel()
		name := "//pkg:secret.go"
		hasher := &fakeSourceHasher{err: assert.AnError} // would fail the test if called
		targets := map[string]*targethasher.Target{
			name: {Name: name, RuleType: targethasher.SourceFileType, SourceFile: &buildpb.SourceFile{}},
		}

		err := computeAvailableHashes(context.Background(), hasher, targets, []*regexp.Regexp{regexp.MustCompile("secret")})
		require.NoError(t, err)
		assert.Equal(t, []byte{}, targets[name].Hash)
		assert.Equal(t, []byte{}, targets[name].HashWithoutDeps)
		assert.Nil(t, hasher.ctx, "hasher should never be invoked for an excluded target")
	})

	t.Run("excluded rule target gets empty hash instead of HashWithoutDeps", func(t *testing.T) {
		t.Parallel()
		name := "//pkg:lib"
		ruleName := name
		ruleClass := "go_library"
		targets := map[string]*targethasher.Target{
			name: {
				Name:     name,
				RuleType: "go_library",
				Rule:     &buildpb.Rule{Name: &ruleName, RuleClass: &ruleClass},
			},
		}

		err := computeAvailableHashes(context.Background(), &fakeSourceHasher{}, targets, []*regexp.Regexp{regexp.MustCompile("//pkg:lib")})
		require.NoError(t, err)
		assert.Equal(t, []byte{}, targets[name].Hash)
		assert.Equal(t, []byte{}, targets[name].HashWithoutDeps)
	})

	t.Run("non-matching excludedRegex leaves hashing unaffected", func(t *testing.T) {
		t.Parallel()
		name := "//pkg:__pkg__"
		targets := map[string]*targethasher.Target{
			name: {Name: name, RuleType: targethasher.PackageGroup},
		}

		err := computeAvailableHashes(context.Background(), &fakeSourceHasher{}, targets, []*regexp.Regexp{regexp.MustCompile("no-match")})
		require.NoError(t, err)

		h := sha1.New()
		h.Write([]byte(name))
		assert.Equal(t, h.Sum(nil), targets[name].Hash)
	})
}

func TestIsExcludedTarget(t *testing.T) {
	t.Parallel()

	t.Run("empty regex list never excludes", func(t *testing.T) {
		t.Parallel()
		assert.False(t, isExcludedTarget("//pkg:anything", nil))
	})

	t.Run("matches when any regex in the list matches", func(t *testing.T) {
		t.Parallel()
		regexes := []*regexp.Regexp{
			regexp.MustCompile("^//other:"),
			regexp.MustCompile("^//pkg:target$"),
		}
		assert.True(t, isExcludedTarget("//pkg:target", regexes))
	})

	t.Run("returns false when no regex matches", func(t *testing.T) {
		t.Parallel()
		regexes := []*regexp.Regexp{
			regexp.MustCompile("^//other:"),
			regexp.MustCompile("^//another:"),
		}
		assert.False(t, isExcludedTarget("//pkg:target", regexes))
	})
}

// --- computeHashes ---

func TestComputeHashes(t *testing.T) {
	t.Parallel()

	t.Run("context cancelled returns error", func(t *testing.T) {
		t.Parallel()
		g := OptimizeGraph(map[string]*targethasher.Target{
			"//pkg:a": {Name: "//pkg:a", RuleType: "go_library"},
		})
		aID := g.TargetNameToID["//pkg:a"]

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := g.computeHashes(ctx, aID, nil)
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("target with existing hash returned immediately", func(t *testing.T) {
		t.Parallel()
		hash := []byte{0xDE, 0xAD}
		g := OptimizeGraph(map[string]*targethasher.Target{
			"//pkg:a": {Name: "//pkg:a", RuleType: "go_library", Hash: hash},
		})
		aID := g.TargetNameToID["//pkg:a"]

		got, err := g.computeHashes(context.Background(), aID, nil)
		require.NoError(t, err)
		assert.Equal(t, hash, got)
	})

	t.Run("missing target ID returns error", func(t *testing.T) {
		t.Parallel()
		g := OptimizeGraph(nil)

		_, err := g.computeHashes(context.Background(), 9999, nil)
		assert.Error(t, err)
	})

	t.Run("external rule target returns existing hash", func(t *testing.T) {
		t.Parallel()
		hash := []byte{0xCA, 0xFE}
		g := OptimizeGraph(map[string]*targethasher.Target{
			"//external:repo": {Name: "//external:repo", RuleType: targethasher.ExternalRuleType, Hash: hash},
		})
		id := g.TargetNameToID["//external:repo"]

		got, err := g.computeHashes(context.Background(), id, nil)
		require.NoError(t, err)
		assert.Equal(t, hash, got)
	})

	t.Run("source file with nil hash returns error", func(t *testing.T) {
		t.Parallel()
		g := OptimizeGraph(map[string]*targethasher.Target{
			"//pkg:f.go": {Name: "//pkg:f.go", RuleType: targethasher.SourceFileType}, // Hash intentionally nil
		})
		id := g.TargetNameToID["//pkg:f.go"]

		_, err := g.computeHashes(context.Background(), id, nil)
		assert.Error(t, err, "source file should already have its hash set")
	})

	t.Run("generated file inherits dep hash", func(t *testing.T) {
		t.Parallel()
		depHash := []byte{0x01, 0x02, 0x03}
		g := OptimizeGraph(map[string]*targethasher.Target{
			"//pkg:rule": {Name: "//pkg:rule", RuleType: "go_library", Hash: []byte{0xFF}},
			"//pkg:gen":  {Name: "//pkg:gen", RuleType: targethasher.GeneratedFileType, Deps: []string{"//pkg:rule"}},
		})
		ruleID := g.TargetNameToID["//pkg:rule"]
		genID := g.TargetNameToID["//pkg:gen"]

		// Override the rule hash directly so we know what to expect
		g.OptimizedTargets[ruleID].Hash = depHash
		g.OptimizedTargets[genID].Hash = nil // force recompute

		got, err := g.computeHashes(context.Background(), genID, nil)
		require.NoError(t, err)
		assert.Equal(t, depHash, got, "generated file should inherit its dep's hash")
	})

	t.Run("rule target hash combines HashWithoutDeps and dep hashes", func(t *testing.T) {
		t.Parallel()
		hwod := []byte{0x03, 0x04}
		depHash := []byte{0x01, 0x02}
		g := OptimizeGraph(map[string]*targethasher.Target{
			"//pkg:dep": {Name: "//pkg:dep", RuleType: "go_library", Hash: depHash},
			"//pkg:lib": {Name: "//pkg:lib", RuleType: "go_library", HashWithoutDeps: hwod, Deps: []string{"//pkg:dep"}},
		})
		libID := g.TargetNameToID["//pkg:lib"]
		// lib has no final hash yet (only HashWithoutDeps)
		g.OptimizedTargets[libID].Hash = nil

		got, err := g.computeHashes(context.Background(), libID, nil)
		require.NoError(t, err)

		// Expected: sha1(hwod || depHash)  (single dep, already sorted)
		h := sha1.New()
		h.Write(hwod)
		h.Write(depHash)
		assert.Equal(t, h.Sum(nil), got)
	})

	t.Run("excluded target short-circuits before dep traversal", func(t *testing.T) {
		t.Parallel()
		g := OptimizeGraph(map[string]*targethasher.Target{
			"//pkg:lib": {
				Name:            "//pkg:lib",
				RuleType:        "go_library",
				HashWithoutDeps: []byte{0x01},
				Deps:            []string{"//pkg:missing"}, // dangling: never added to the graph
			},
		})
		libID := g.TargetNameToID["//pkg:lib"]
		g.OptimizedTargets[libID].Hash = nil // force recompute path

		got, err := g.computeHashes(context.Background(), libID, []*regexp.Regexp{regexp.MustCompile("//pkg:lib")})
		require.NoError(t, err, "excluded targets should short-circuit before resolving deps")
		assert.Equal(t, []byte{}, got)

		// Sanity check: without the exclusion, the same dangling dep does error.
		g2 := OptimizeGraph(map[string]*targethasher.Target{
			"//pkg:lib": {
				Name:            "//pkg:lib",
				RuleType:        "go_library",
				HashWithoutDeps: []byte{0x01},
				Deps:            []string{"//pkg:missing"},
			},
		})
		libID2 := g2.TargetNameToID["//pkg:lib"]
		g2.OptimizedTargets[libID2].Hash = nil
		_, err = g2.computeHashes(context.Background(), libID2, nil)
		assert.Error(t, err, "unexcluded target with a dangling dep should still error")
	})
}

func TestComputeInvalidatedHashesCycleOrderInvariance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		newTargets func() map[string]*targethasher.Target
		fullRoot   string
	}{
		{
			name: "rooted cycle matches full recomputation",
			newTargets: func() map[string]*targethasher.Target {
				return map[string]*targethasher.Target{
					"//pkg:a": {
						Name:            "//pkg:a",
						RuleType:        "go_library",
						HashWithoutDeps: []byte("a"),
						Deps:            []string{"//pkg:b"},
					},
					"//pkg:b": {
						Name:            "//pkg:b",
						RuleType:        "go_library",
						HashWithoutDeps: []byte("b"),
						Deps:            []string{"//pkg:a"},
					},
					"//pkg:root": {
						Name:            "//pkg:root",
						RuleType:        "go_library",
						HashWithoutDeps: []byte("root"),
						Deps:            []string{"//pkg:b"},
					},
				}
			},
			fullRoot: "//pkg:root",
		},
		{
			name: "rootless cycle uses canonical member",
			newTargets: func() map[string]*targethasher.Target {
				return map[string]*targethasher.Target{
					"//pkg:a": {
						Name:            "//pkg:a",
						RuleType:        "go_library",
						HashWithoutDeps: []byte("a"),
						Deps:            []string{"//pkg:b"},
					},
					"//pkg:b": {
						Name:            "//pkg:b",
						RuleType:        "go_library",
						HashWithoutDeps: []byte("b"),
						Deps:            []string{"//pkg:a"},
					},
				}
			},
			fullRoot: "//pkg:a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			expectedTargets := tt.newTargets()
			_, err := targethasher.HashRecursively(t.Context(), targethasher.HashParam{
				Targets:    expectedTargets,
				TargetName: tt.fullRoot,
			})
			require.NoError(t, err)

			names := make([]string, 0, len(expectedTargets))
			expectedHashes := make(map[string][]byte, len(expectedTargets))
			for name, target := range expectedTargets {
				names = append(names, name)
				expectedHashes[name] = target.Hash
			}

			ctx := t.Context()
			rapid.Check(t, func(rt *rapid.T) {
				graph := OptimizeGraph(tt.newTargets())
				invalidated := NewIntSet()
				for _, name := range rapid.Permutation(names).Draw(rt, "invalidation-order") {
					invalidated.Insert(graph.TargetNameToID[name])
				}

				err := graph.computeInvalidatedHashes(ctx, invalidated, nil)
				require.NoError(rt, err)
				for name, expected := range expectedHashes {
					actual := graph.OptimizedTargets[graph.TargetNameToID[name]].Hash
					assert.Equal(rt, expected, actual, "hash mismatch for %s", name)
				}
			})
		})
	}
}
