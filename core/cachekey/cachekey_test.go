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
	treehash := "abcd1234"
	strategy := entity.ComputationStrategyNative
	repositoryID := "test-repository"

	// Nil/empty exclude list ⇒ no suffix.
	got := GetGraphByTreeHash(repositoryID, treehash, strategy, nil)
	assert.Equal(t, filepath.Join(repositoryID, "graphs", treehash, strategy.String()), got)
	assert.Equal(t, got, GetGraphByTreeHash(repositoryID, treehash, strategy, []string{}))

	// Different strategies ⇒ different keys.
	assert.NotEqual(t, got, GetGraphByTreeHash(repositoryID, treehash, entity.ComputationStrategyShell, nil))

	// Non-empty list ⇒ suffix appended; different lists ⇒ different keys.
	withFoo := GetGraphByTreeHash(repositoryID, treehash, strategy, []string{"foo.*"})
	assert.NotEqual(t, got, withFoo)
	assert.NotEqual(t, withFoo, GetGraphByTreeHash(repositoryID, treehash, strategy, []string{"bar.*"}))
	// Order-independence: sort before hashing.
	assert.Equal(t,
		GetGraphByTreeHash(repositoryID, treehash, strategy, []string{"a", "b"}),
		GetGraphByTreeHash(repositoryID, treehash, strategy, []string{"b", "a"}),
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
	repositoryID := "test-repository"
	got := GetTreehashCachePath(repositoryID, desc)
	want := filepath.Join(repositoryID, "treehashes", "base-sha-deadbeef") + "_request-urls-" + url.GetReqURLsHash(desc.ChangeRequests)
	assert.Equal(t, want, got)
}

func TestGetComparedTargetsCachePath(t *testing.T) {
	t.Parallel()
	repositoryID := "test-repository"
	got := GetComparedTargetsCachePath(repositoryID, "abc", "def", nil)
	assert.Equal(t, filepath.Join(repositoryID, "compared-targets", "abc_def"), got)

	// Nil/empty list ⇒ legacy path.
	assert.Equal(t, got, GetComparedTargetsCachePath(repositoryID, "abc", "def", []string{}))

	// Different exclude lists ⇒ different keys.
	assert.NotEqual(t, got, GetComparedTargetsCachePath(repositoryID, "abc", "def", []string{"foo.*"}))
}

func TestGetTGBGraphByTreeHash(t *testing.T) {
	t.Parallel()
	treehash := "abcd1234"
	strategy := entity.ComputationStrategyNative
	repositoryID := "test-repository"

	got := GetTGBGraphByTreeHash(repositoryID, treehash, strategy, nil)
	assert.Equal(t, filepath.Join(repositoryID, "graphs", treehash, strategy.String()+"-tgb"), got)
	assert.Equal(t, got, GetTGBGraphByTreeHash(repositoryID, treehash, strategy, []string{}))

	// Never collides with the gob key space, and stays a sibling (same
	// directory) rather than a child of the gob key — on the disk backend a
	// key is a file, so a child key could not coexist with the gob blob.
	gob := GetGraphByTreeHash(repositoryID, treehash, strategy, nil)
	assert.NotEqual(t, gob, got)
	assert.Equal(t, filepath.Dir(gob), filepath.Dir(got))

	// Exclude-files regex folds into the key the same way as the gob variant.
	withRegex := GetTGBGraphByTreeHash(repositoryID, treehash, strategy, []string{"^docs/"})
	assert.NotEqual(t, got, withRegex)
	assert.Contains(t, withRegex, "_requests-options-")
}
