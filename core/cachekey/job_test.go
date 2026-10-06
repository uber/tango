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

	"github.com/stretchr/testify/assert"
	"github.com/uber/tango/entity"
)

func TestGetTargetGraphJobToken(t *testing.T) {
	t.Parallel()
	base := entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{
			Remote:   "git@github:uber/tango",
			BaseSha:  "deadbeef",
			Strategy: entity.ComputationStrategyNative,
		},
	}

	got := GetTargetGraphJobToken(base)
	assert.Equal(t, got, GetTargetGraphJobToken(base), "deterministic for identical input")

	diffSha := base
	diffSha.Build.BaseSha = "cafebabe"
	assert.NotEqual(t, got, GetTargetGraphJobToken(diffSha))

	diffStrategy := base
	diffStrategy.Build.Strategy = entity.ComputationStrategyShell
	assert.NotEqual(t, got, GetTargetGraphJobToken(diffStrategy))

	diffRegex := base
	diffRegex.ExcludeFilesRegex = []string{"foo.*"}
	assert.NotEqual(t, got, GetTargetGraphJobToken(diffRegex))

	diffBypass := base
	diffBypass.BypassCache = true
	assert.NotEqual(t, got, GetTargetGraphJobToken(diffBypass))
}

func TestGetChangedTargetsJobToken(t *testing.T) {
	t.Parallel()
	first := entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "sha1"}
	second := entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "sha2"}

	got := GetChangedTargetsJobToken(first, second, nil, false)
	assert.Equal(t, got, GetChangedTargetsJobToken(first, second, nil, false), "deterministic for identical input")

	assert.NotEqual(t, got, GetChangedTargetsJobToken(second, first, nil, false), "revision order matters")

	assert.NotEqual(t, got, GetChangedTargetsJobToken(first, second, nil, true))
}
