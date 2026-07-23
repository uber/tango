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
	"errors"
	"fmt"

	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/repomanager"
)

func classifyLeaseError(err error) error {
	wrappedErr := fmt.Errorf("lease workspace: %w", err)
	if errors.Is(err, repomanager.ErrPoolTimeout) {
		return tangoerrors.NewInfraRetryable(wrappedErr)
	}
	return tangoerrors.NewInfra(wrappedErr)
}

// classifyGitError wraps err with the given message and classifies it as an
// infra error when it was caused by a git command timing out.
func classifyGitError(msg string, err error) error {
	wrappedErr := fmt.Errorf("%s: %w", msg, err)
	if errors.Is(err, git.ErrTimeout) {
		return tangoerrors.NewInfra(wrappedErr)
	}
	return wrappedErr
}
