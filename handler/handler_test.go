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

func TestResolveRequestRepository(t *testing.T) {
	tests := []struct {
		name       string
		provider   config.RepositoryConfigProvider
		remote     string
		requestErr error
		wantLabel  string
		wantErr    bool
		wantCode   tangoerrors.ErrorCode
	}{
		{
			name:       "request error uses unknown repository",
			provider:   noRepositoryConfigProvider{},
			remote:     "ignored",
			requestErr: assert.AnError,
			wantLabel:  unknownRepositoryMetricLabel,
			wantErr:    true,
		},
		{
			name:      "plain remote",
			provider:  noRepositoryConfigProvider{},
			remote:    "git@github.com:other/repo.git",
			wantLabel: unknownRepositoryMetricLabel,
			wantErr:   true,
			wantCode:  tangoerrors.ErrorUser,
		},
		{
			name:      "configured remote uses the repository ID",
			provider:  allowAnyRepositoryConfigProvider{},
			remote:    "git@github.com:other/repo.git",
			wantLabel: "test-repository",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &handler{repoConfig: tt.provider}
			repo, label, err := h.resolveRequestRepository(tt.remote, tt.requestErr)
			assert.Equal(t, tt.wantLabel, label)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			assert.Empty(t, repo)
			require.Error(t, err)
			if tt.requestErr != nil {
				assert.ErrorIs(t, err, tt.requestErr)
				return
			}
			assert.Equal(t, tt.wantCode, tangoerrors.GetErrorCode(err))
		})
	}
}
