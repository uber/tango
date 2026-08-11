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

package workspace

import (
	"context"
	"fmt"

	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/workspace/githubpr"
	"go.uber.org/zap"
)

// Request represents a change request that can be applied to a workspace.
type Request interface {
	Apply(ctx context.Context) error
}

// NewRequest creates a Request from a canonical change URI and the remote
// URL of the repository. Currently only GitHub PR URIs are supported
// (github://{host}/{org}/{repo}/pull/{pr}/{head_sha}).
func NewRequest(rawURL string, g git.Interface, remote string, baseRef string, logger *zap.Logger) (Request, error) {
	pr, err := githubpr.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid change URI %q: %w", rawURL, err)
	}
	return newGitHubPRRequest(g, remote, pr, baseRef, logger), nil
}
