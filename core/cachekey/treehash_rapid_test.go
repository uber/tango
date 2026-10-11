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

func TestGetTreehashCachePath_headSHAAffectsKey(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		sha := rapid.StringMatching(`[0-9a-f]{40}`).Draw(t, "sha")
		otherSHA := rapid.StringMatching(`[0-9a-f]{40}`).Filter(func(s string) bool { return s != sha }).Draw(t, "otherSHA")
		build := entity.BuildDescription{
			Remote:  "git@github:uber/tango",
			BaseSha: "base",
			ChangeRequests: []entity.ChangeRequest{
				{URL: "github://github.com/uber/tango/pull/1/" + sha},
			},
		}
		changed := build
		changed.ChangeRequests = []entity.ChangeRequest{
			{URL: "github://github.com/uber/tango/pull/1/" + otherSHA},
		}

		repositoryID := "test-repository"
		require.NotEqual(t, GetTreehashCachePath(repositoryID, build), GetTreehashCachePath(repositoryID, changed))
	})
}

func TestGetTreehashCachePath_changeRequestOrderAffectsKey(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		firstSHA := rapid.StringMatching(`[0-9a-f]{40}`).Draw(t, "firstSHA")
		secondSHA := rapid.StringMatching(`[0-9a-f]{40}`).Filter(func(s string) bool { return s != firstSHA }).Draw(t, "secondSHA")
		build := entity.BuildDescription{
			Remote:  "git@github:uber/tango",
			BaseSha: "base",
			ChangeRequests: []entity.ChangeRequest{
				{URL: "github://github.com/uber/tango/pull/1/" + firstSHA},
				{URL: "github://github.com/uber/tango/pull/2/" + secondSHA},
			},
		}
		swapped := build
		swapped.ChangeRequests = []entity.ChangeRequest{
			build.ChangeRequests[1],
			build.ChangeRequests[0],
		}

		repositoryID := "test-repository"
		require.NotEqual(t, GetTreehashCachePath(repositoryID, build), GetTreehashCachePath(repositoryID, swapped))
	})
}
