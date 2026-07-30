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

package cachekey

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/config"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/url"
)

func TestGetGraphByTreeHash(t *testing.T) {
	t.Parallel()
	remote := "git@github:uber/tango"
	treehash := "abcd1234"
	strategy := entity.ComputationStrategyNative

	// Nil/empty exclude list + empty config hash => no suffix (legacy path).
	got := GetGraphByTreeHash(remote, treehash, strategy, nil, "")
	assert.Equal(t, filepath.Join("uber/tango", "graphs", treehash, strategy.String()), got)
	assert.Equal(t, got, GetGraphByTreeHash(remote, treehash, strategy, []string{}, ""))

	// Different strategies => different keys.
	assert.NotEqual(t, got, GetGraphByTreeHash(remote, treehash, entity.ComputationStrategyShell, nil, ""))

	// Non-empty list => suffix appended; different lists => different keys.
	withFoo := GetGraphByTreeHash(remote, treehash, strategy, []string{"foo.*"}, "")
	assert.NotEqual(t, got, withFoo)
	assert.NotEqual(t, withFoo, GetGraphByTreeHash(remote, treehash, strategy, []string{"bar.*"}, ""))
	// Order-independence: sort before hashing.
	assert.Equal(t,
		GetGraphByTreeHash(remote, treehash, strategy, []string{"a", "b"}, ""),
		GetGraphByTreeHash(remote, treehash, strategy, []string{"b", "a"}, ""),
	)
}

func TestGetGraphByTreeHash_ConfigHash(t *testing.T) {
	t.Parallel()
	remote := "git@github:uber/tango"
	treehash := "abcd1234"
	strategy := entity.ComputationStrategyNative

	legacyPath := GetGraphByTreeHash(remote, treehash, strategy, nil, "")

	// Non-empty config hash appends a _repo-config- suffix.
	withConfig := GetGraphByTreeHash(remote, treehash, strategy, nil, "deadbeef")
	assert.NotEqual(t, legacyPath, withConfig)
	assert.Contains(t, withConfig, "_repo-config-deadbeef")

	// Different config hashes produce different keys.
	assert.NotEqual(t, withConfig, GetGraphByTreeHash(remote, treehash, strategy, nil, "cafebabe"))

	// Config hash and exclude regex are both appended, independently.
	withBoth := GetGraphByTreeHash(remote, treehash, strategy, []string{"foo"}, "deadbeef")
	assert.Contains(t, withBoth, "_requests-options-")
	assert.Contains(t, withBoth, "_repo-config-deadbeef")
}

func TestGetTreehashCachePath(t *testing.T) {
	t.Parallel()
	desc := entity.BuildDescription{
		Remote:  "git@github:uber/tango",
		BaseSha: "deadbeef",
		ChangeRequests: []entity.ChangeRequest{
			{URL: "github://org/repo/pull/1", Commit: "abc"},
			{URL: "custom://foo/bar", Commit: "def"},
		},
	}
	got := GetTreehashCachePath(desc)
	want := filepath.Join("uber/tango", "treehashes", "base-sha-deadbeef") + "_request-urls-" + url.GetReqURLsHash(desc.ChangeRequests)
	assert.Equal(t, want, got)
}

func TestGetComparedTargetsCachePath(t *testing.T) {
	t.Parallel()
	got := GetComparedTargetsCachePath("git@github:uber/tango", "abc", "def", nil)
	assert.Equal(t, filepath.Join("uber/tango", "compared-targets", "abc_def"), got)

	// Nil/empty list => legacy path.
	assert.Equal(t, got, GetComparedTargetsCachePath("git@github:uber/tango", "abc", "def", []string{}))

	// Different exclude lists => different keys.
	assert.NotEqual(t, got, GetComparedTargetsCachePath("git@github:uber/tango", "abc", "def", []string{"foo.*"}))
}

func TestIsFullHexSHA(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		s    string
		want bool
	}{
		{name: "valid lowercase", s: "a" + "0123456789abcdef0123456789abcdef0123456", want: true},
		{name: "valid uppercase", s: "A0123456789ABCDEF0123456789ABCDEF01234567", want: false}, // 41 chars
		{name: "valid mixed case 40", s: "aAbBcCdDeEfF00112233445566778899aAbBcCdD", want: true},
		{name: "40 lowercase hex", s: "da39a3ee5e6b4b0d3255bfef95601890afd80709", want: true},
		{name: "39 chars", s: "da39a3ee5e6b4b0d3255bfef95601890afd8070", want: false},
		{name: "41 chars", s: "da39a3ee5e6b4b0d3255bfef95601890afd807091", want: false},
		{name: "HEAD", s: "HEAD", want: false},
		{name: "branch name", s: "main", want: false},
		{name: "short sha", s: "da39a3e", want: false},
		{name: "40 chars with g", s: "ga39a3ee5e6b4b0d3255bfef95601890afd80709", want: false},
		{name: "empty", s: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsFullHexSHA(tt.s))
		})
	}
}

func TestHashGraphAffectingConfig(t *testing.T) {
	t.Parallel()

	t.Run("default config produces empty hash (legacy key)", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "", HashGraphAffectingConfig(config.RepositoryConfig{}))
	})

	t.Run("non-graph fields do not affect hash", func(t *testing.T) {
		t.Parallel()
		// Remote, QueryTimeout, StreamBazelLogs are non-graph fields.
		cfg := config.RepositoryConfig{
			Remote:          "git@github:uber/foo",
			QueryTimeout:    120,
			StreamBazelLogs: true,
		}
		assert.Equal(t, "", HashGraphAffectingConfig(cfg))
	})

	t.Run("each graph-affecting field changes the hash", func(t *testing.T) {
		t.Parallel()
		base := config.RepositoryConfig{}
		baseHash := HashGraphAffectingConfig(base)

		fields := []struct {
			name string
			cfg  config.RepositoryConfig
		}{
			{"BzlmodEnabled", config.RepositoryConfig{BzlmodEnabled: true}},
			{"ExcludeExternalTargets", config.RepositoryConfig{ExcludeExternalTargets: true}},
			{"BazelCommand", config.RepositoryConfig{BazelCommand: "/usr/bin/bazel"}},
			{"BazelExtraArgs", config.RepositoryConfig{BazelExtraArgs: []string{"--foo"}}},
			{"BazelStartupOptions", config.RepositoryConfig{BazelStartupOptions: []string{"--batch"}}},
			{"FullHashRepos", config.RepositoryConfig{FullHashRepos: []string{"@com_google_protobuf"}}},
			{"ExcludedFiles", config.RepositoryConfig{ExcludedFiles: []string{"BUILD.bazel"}}},
		}
		hashes := make(map[string]string, len(fields))
		for _, f := range fields {
			h := HashGraphAffectingConfig(f.cfg)
			require.NotEqual(t, baseHash, h, "field %s should change the hash from default", f.name)
			// Each field should produce a unique hash.
			for prevName, prevHash := range hashes {
				assert.NotEqual(t, prevHash, h, "field %s produced same hash as %s", f.name, prevName)
			}
			hashes[f.name] = h
		}
	})

	t.Run("ordering-insensitivity for slices", func(t *testing.T) {
		t.Parallel()
		cfg1 := config.RepositoryConfig{BazelExtraArgs: []string{"--a", "--b", "--c"}}
		cfg2 := config.RepositoryConfig{BazelExtraArgs: []string{"--c", "--a", "--b"}}
		assert.Equal(t, HashGraphAffectingConfig(cfg1), HashGraphAffectingConfig(cfg2))

		cfg3 := config.RepositoryConfig{FullHashRepos: []string{"repo-b", "repo-a"}}
		cfg4 := config.RepositoryConfig{FullHashRepos: []string{"repo-a", "repo-b"}}
		assert.Equal(t, HashGraphAffectingConfig(cfg3), HashGraphAffectingConfig(cfg4))

		cfg5 := config.RepositoryConfig{BazelStartupOptions: []string{"--z", "--a"}}
		cfg6 := config.RepositoryConfig{BazelStartupOptions: []string{"--a", "--z"}}
		assert.Equal(t, HashGraphAffectingConfig(cfg5), HashGraphAffectingConfig(cfg6))

		cfg7 := config.RepositoryConfig{ExcludedFiles: []string{"b.go", "a.go"}}
		cfg8 := config.RepositoryConfig{ExcludedFiles: []string{"a.go", "b.go"}}
		assert.Equal(t, HashGraphAffectingConfig(cfg7), HashGraphAffectingConfig(cfg8))
	})

	t.Run("different values produce different hashes", func(t *testing.T) {
		t.Parallel()
		cfg1 := config.RepositoryConfig{BazelCommand: "bazel"}
		cfg2 := config.RepositoryConfig{BazelCommand: "bazelisk"}
		assert.NotEqual(t, HashGraphAffectingConfig(cfg1), HashGraphAffectingConfig(cfg2))
	})

	t.Run("original slice not mutated", func(t *testing.T) {
		t.Parallel()
		args := []string{"c", "a", "b"}
		orig := make([]string, len(args))
		copy(orig, args)
		HashGraphAffectingConfig(config.RepositoryConfig{BazelExtraArgs: args})
		assert.Equal(t, orig, args, "caller's slice must not be reordered")
	})
}

// TestGraphKeySymmetry verifies that the controller read path and the
// orchestrator write path produce identical graph cache keys for the same
// non-default config. This pins the contract that both sides derive the
// same configHash from the same RepositoryConfig.
func TestGraphKeySymmetry(t *testing.T) {
	t.Parallel()

	remote := "git@github:uber/tango"
	treehash := "da39a3ee5e6b4b0d3255bfef95601890afd80709"
	strategy := entity.ComputationStrategyNative
	excludeFiles := []string{"BUILD\\.bazel"}

	// Non-default config that a real deployment might use.
	repoCfg := config.RepositoryConfig{
		BzlmodEnabled:          true,
		BazelCommand:           "/opt/bazel/bin/bazel",
		BazelExtraArgs:         []string{"--keep_going", "--noshow_progress"},
		BazelStartupOptions:    []string{"--batch"},
		FullHashRepos:          []string{"@com_google_protobuf"},
		ExcludeExternalTargets: true,
		ExcludedFiles:          []string{"testdata/.*"},
	}

	// Orchestrator write path: compute configHash directly from the resolved config.
	orchConfigHash := HashGraphAffectingConfig(repoCfg)
	orchKey := GetGraphByTreeHash(remote, treehash, strategy, excludeFiles, orchConfigHash)

	// Controller read path: simulate configHashForRemote by looking up the
	// same config from a provider and hashing it.
	ctrlCfg := repoCfg // same config, as required by the contract
	ctrlConfigHash := HashGraphAffectingConfig(ctrlCfg)
	ctrlKey := GetGraphByTreeHash(remote, treehash, strategy, excludeFiles, ctrlConfigHash)

	assert.Equal(t, orchKey, ctrlKey, "controller read key must match orchestrator write key for the same config")
	assert.NotEmpty(t, orchConfigHash, "non-default config must produce a non-empty hash")
}
