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

package orchestrator

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/core/repomanager"
)

func TestClassifyLeaseError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		err          error
		wantCode     tangoerrors.ErrorCode
		wantContains string
	}{
		{
			name:         "pool timeout is infra retryable",
			err:          fmt.Errorf("%w: pool for repo org/repo: context deadline exceeded", repomanager.ErrPoolTimeout),
			wantCode:     tangoerrors.ErrorInfraRetryable,
			wantContains: "lease workspace",
		},
		{
			name:         "generic error is infra",
			err:          fmt.Errorf("clone origin: connection refused"),
			wantCode:     tangoerrors.ErrorInfra,
			wantContains: "lease workspace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := classifyLeaseError(tt.err)
			require.Error(t, got)
			assert.Equal(t, tt.wantCode, tangoerrors.GetErrorCode(got))
			assert.Contains(t, got.Error(), tt.wantContains)
		})
	}
}
