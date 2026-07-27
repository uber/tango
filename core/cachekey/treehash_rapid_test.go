// Copyright (c) 2026 Uber Technologies, Inc.
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
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uber/tango/entity"
	"pgregory.net/rapid"
)

func TestGetTreehashCachePath_commitAffectsKey(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		commit := rapid.StringMatching(`[a-z]+`).Draw(t, "commit")
		build := entity.BuildDescription{
			Remote:  "git@github:uber/tango",
			BaseSha: "base",
			ChangeRequests: []entity.ChangeRequest{
				{URL: "github://uber/tango/pull/1", Commit: commit},
			},
		}
		changed := build
		changed.ChangeRequests = append([]entity.ChangeRequest(nil), build.ChangeRequests...)
		changed.ChangeRequests[0].Commit = commit + "x"

		require.NotEqual(t, GetTreehashCachePath(build), GetTreehashCachePath(changed))
	})
}

func TestGetTreehashCachePath_changeRequestOrderAffectsKey(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		firstCommit := rapid.StringMatching(`[a-z]+`).Draw(t, "firstCommit")
		secondCommit := rapid.StringMatching(`[a-z]+`).Draw(t, "secondCommit")
		build := entity.BuildDescription{
			Remote:  "git@github:uber/tango",
			BaseSha: "base",
			ChangeRequests: []entity.ChangeRequest{
				{URL: "github://uber/tango/pull/1", Commit: firstCommit},
				{URL: "github://uber/tango/pull/2", Commit: secondCommit},
			},
		}
		swapped := build
		swapped.ChangeRequests = []entity.ChangeRequest{
			build.ChangeRequests[1],
			build.ChangeRequests[0],
		}

		require.NotEqual(t, GetTreehashCachePath(build), GetTreehashCachePath(swapped))
	})
}
