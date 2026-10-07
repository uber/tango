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

func TestExcludeFilesRegex_keyProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := "a" + rapid.StringMatching(`[a-m]*`).Draw(t, "aSuffix")
		b := "n" + rapid.StringMatching(`[a-m]*`).Draw(t, "bSuffix")
		c := "z" + rapid.StringMatching(`[a-m]*`).Draw(t, "cSuffix")
		left := []string{a + b, c}
		right := []string{a, b + c}
		leftBefore := append([]string(nil), left...)
		rightBefore := append([]string(nil), right...)
		leftPermuted := []string{left[1], left[0]}
		rightPermuted := []string{right[1], right[0]}
		repositoryID := "test-repository"

		leftGraph := GetGraphByTreeHash(repositoryID, "tree", entity.ComputationStrategyNative, left)
		rightGraph := GetGraphByTreeHash(repositoryID, "tree", entity.ComputationStrategyNative, right)
		require.Equal(t, leftGraph, GetGraphByTreeHash(repositoryID, "tree", entity.ComputationStrategyNative, leftPermuted))

		leftCompared := GetComparedTargetsCachePath(repositoryID, "before", "after", left)
		rightCompared := GetComparedTargetsCachePath(repositoryID, "before", "after", right)
		require.Equal(t, leftCompared, GetComparedTargetsCachePath(repositoryID, "before", "after", leftPermuted))
		require.Equal(t, rightGraph, GetGraphByTreeHash(repositoryID, "tree", entity.ComputationStrategyNative, rightPermuted))
		require.Equal(t, rightCompared, GetComparedTargetsCachePath(repositoryID, "before", "after", rightPermuted))
		require.Equal(t, leftBefore, left)
		require.Equal(t, rightBefore, right)
		require.NotEqual(t, leftGraph, rightGraph)
		require.NotEqual(t, leftCompared, rightCompared)
	})
}
