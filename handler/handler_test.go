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

package handler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/config"
	tangoerrors "github.com/uber/tango/core/errors"
)

type rejectAllRepositoryConfigProvider struct{}

func (rejectAllRepositoryConfigProvider) GetRepositoryConfig(string) (config.RepositoryConfig, bool) {
	return config.RepositoryConfig{}, false
}

func TestResolveRequestRepository(t *testing.T) {
	h := &handler{repoConfig: rejectAllRepositoryConfigProvider{}}

	t.Run("request error uses unknown repository", func(t *testing.T) {
		repo, label, err := h.resolveRequestRepository("ignored", assert.AnError)
		assert.Empty(t, repo)
		assert.Equal(t, unknownRepositoryMetricLabel, label)
		assert.ErrorIs(t, err, assert.AnError)
	})

	t.Run("plain remote", func(t *testing.T) {
		_, label, err := h.resolveRequestRepository("git@github.com:other/repo.git", nil)
		require.Error(t, err)
		assert.Equal(t, unknownRepositoryMetricLabel, label)
		assert.Equal(t, tangoerrors.ErrorUser, tangoerrors.GetErrorCode(err))
	})
}
