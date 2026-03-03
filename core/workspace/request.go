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
	"net/url"

	"github.com/uber/tango/core/git"
)

// Request represents a change request type, like Phabricator diff or Github pull request
type Request interface {
	Apply(ctx context.Context) error
}

// NewRequest creates a new request based on the request URL.
func NewRequest(rawURL string, g git.Interface, baseRef string, commit string) (Request, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "github":
		return NewGitRequest(g, u.Path, baseRef, commit), nil
	}
	return nil, nil
}
