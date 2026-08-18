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
	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/url"
)

func TestGetGraphByTreeHash(t *testing.T) {
	t.Parallel()
	remote := "git@github:uber/tango"
	treehash := "abcd1234"
	strategy := entity.ComputationStrategyNative

	// Nil/empty exclude list ⇒ no suffix.
	got := GetGraphByTreeHash(remote, treehash, strategy, nil)
	assert.Equal(t, filepath.Join("uber/tango", "graphs", treehash, strategy.String()), got)
	assert.Equal(t, got, GetGraphByTreeHash(remote, treehash, strategy, []string{}))

	// Different strategies ⇒ different keys.
	assert.NotEqual(t, got, GetGraphByTreeHash(remote, treehash, entity.ComputationStrategyShell, nil))

	// Non-empty list ⇒ suffix appended; different lists ⇒ different keys.
	withFoo := GetGraphByTreeHash(remote, treehash, strategy, []string{"foo.*"})
	assert.NotEqual(t, got, withFoo)
	assert.NotEqual(t, withFoo, GetGraphByTreeHash(remote, treehash, strategy, []string{"bar.*"}))
	// Order-independence: sort before hashing.
	assert.Equal(t,
		GetGraphByTreeHash(remote, treehash, strategy, []string{"a", "b"}),
		GetGraphByTreeHash(remote, treehash, strategy, []string{"b", "a"}),
	)
}

func TestGetTreehashCachePath(t *testing.T) {
	t.Parallel()
	desc := entity.BuildDescription{
		Remote:  "git@github:uber/tango",
		BaseSha: "deadbeef",
		ChangeRequests: []entity.ChangeRequest{
			{URL: "github://github.com/org/repo/pull/1/1111111111111111111111111111111111111111"},
			{URL: "github://github.com/org/repo/pull/2/2222222222222222222222222222222222222222"},
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

	// Nil/empty list ⇒ legacy path.
	assert.Equal(t, got, GetComparedTargetsCachePath("git@github:uber/tango", "abc", "def", []string{}))

	// Different exclude lists ⇒ different keys.
	assert.NotEqual(t, got, GetComparedTargetsCachePath("git@github:uber/tango", "abc", "def", []string{"foo.*"}))
}

func TestGetAllTargetsChangedCachePath(t *testing.T) {
	t.Parallel()
	got := GetAllTargetsChangedCachePath("git@github:uber/tango", "abc", "def", nil)
	assert.Equal(t, filepath.Join("uber/tango", "all-targets-changed", "abc_def"), got)

	// Nil/empty list ⇒ legacy path.
	assert.Equal(t, got, GetAllTargetsChangedCachePath("git@github:uber/tango", "abc", "def", []string{}))

	// Different file lists ⇒ different keys, so a config change never reads a
	// stale answer computed under a different AllTargetsFiles list.
	assert.NotEqual(t, got, GetAllTargetsChangedCachePath("git@github:uber/tango", "abc", "def", []string{".bazelrc"}))

	// Order-independent: the same set in a different order hashes the same.
	assert.Equal(t,
		GetAllTargetsChangedCachePath("git@github:uber/tango", "abc", "def", []string{"a", "b"}),
		GetAllTargetsChangedCachePath("git@github:uber/tango", "abc", "def", []string{"b", "a"}),
	)

	// Never collides with the compared-targets key space.
	assert.NotEqual(t, got, GetComparedTargetsCachePath("git@github:uber/tango", "abc", "def", nil))
}

func TestGetTGBGraphByTreeHash(t *testing.T) {
	t.Parallel()
	remote := "git@github:uber/tango"
	treehash := "abcd1234"
	strategy := entity.ComputationStrategyNative

	got := GetTGBGraphByTreeHash(remote, treehash, strategy, nil)
	assert.Equal(t, filepath.Join("uber/tango", "graphs", treehash, strategy.String()+"-tgb"), got)
	assert.Equal(t, got, GetTGBGraphByTreeHash(remote, treehash, strategy, []string{}))

	// Never collides with the gob key space, and stays a sibling (same
	// directory) rather than a child of the gob key — on the disk backend a
	// key is a file, so a child key could not coexist with the gob blob.
	gob := GetGraphByTreeHash(remote, treehash, strategy, nil)
	assert.NotEqual(t, gob, got)
	assert.Equal(t, filepath.Dir(gob), filepath.Dir(got))

	// Exclude-files regex folds into the key the same way as the gob variant.
	withRegex := GetTGBGraphByTreeHash(remote, treehash, strategy, []string{"^docs/"})
	assert.NotEqual(t, got, withRegex)
	assert.Contains(t, withRegex, "_requests-options-")
}
