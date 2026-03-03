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
	"go.uber.org/zap"
)

// Workspace defines interface for workspace
type Workspace interface {
	Path() string
	Checkout(ctx context.Context, remote string, ref string) error
	ApplyRequests(ctx context.Context, requests []Request) error
	Release() error
}

type workspace struct {
	path      string
	git       git.Interface
	logger    *zap.SugaredLogger
	onRelease func() // optional callback invoked on Release
}

type WorkspaceParams struct {
	Path      string
	Git       git.Interface
	Logger    *zap.SugaredLogger
	OnRelease func() // if set, called when the workspace is released
}

// NewWorkspace creates a new workspace with the given parameters.
func NewWorkspace(p WorkspaceParams) Workspace {
	return &workspace{
		path:      p.Path,
		git:       p.Git,
		logger:    p.Logger,
		onRelease: p.OnRelease,
	}
}

// Path returns the path of the workspace.
func (w *workspace) Path() string {
	return w.path
}

// ApplyRequests applies the given requests to the workspace.
func (w *workspace) ApplyRequests(ctx context.Context, requests []Request) error {
	for _, request := range requests {
		err := request.Apply(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}

// Checkout checks out the given reference in the workspace.
func (w *workspace) Checkout(ctx context.Context, remote string, ref string) error {
	// check if base ref exists in local repository
	commit := fmt.Sprintf("%s^{commit}", ref)
	_, err := w.git.RevParse(ctx, commit)
	// commit is not present in the repository
	if err != nil {
		w.logger.Warnf("git rev-parse %s failed with error %v, attempt to fetch from remote", commit, err)
		err = w.git.Fetch(ctx, remote, ref)
		if err != nil {
			return err
		}
	}
	return w.git.Checkout(ctx, ref)
}

// Release invokes the onRelease callback if set (e.g., to return the
// workspace to a pool), otherwise it's a no-op.
func (w *workspace) Release() error {
	if w.onRelease != nil {
		w.onRelease()
	}
	return nil
}
