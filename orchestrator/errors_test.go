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
	"context"
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
		name     string
		err      error
		wantCode tangoerrors.ErrorCode
	}{
		{
			name:     "pool timeout is infra retryable",
			err:      fmt.Errorf("%w: %w", repomanager.ErrPoolTimeout, context.DeadlineExceeded),
			wantCode: tangoerrors.ErrorInfraRetryable,
		},
		{
			name:     "generic error is infra",
			err:      fmt.Errorf("clone origin: connection refused"),
			wantCode: tangoerrors.ErrorInfra,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := classifyLeaseError(tt.err)
			require.Error(t, got)
			assert.Equal(t, tt.wantCode, tangoerrors.GetErrorCode(got))
			assert.ErrorIs(t, got, tt.err)
		})
	}
}
