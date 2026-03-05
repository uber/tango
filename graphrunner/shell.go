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

package graphrunner

import (
	"context"
	"errors"

	"github.com/uber/tango/core/targethasher"
	"github.com/uber/tango/core/workspace"
)

// shellGraphRunner computes a target graph by executing an external shell script
// instead of running Bazel queries directly.
type shellGraphRunner struct {
	scriptPath string
}

// ShellGraphRunnerParams are the parameters for constructing a shellGraphRunner.
type ShellGraphRunnerParams struct {
	// ScriptPath is the path to the shell script that produces the target graph.
	ScriptPath string
}

// NewShellGraphRunner creates a GraphRunner that delegates graph computation
// to an external shell script.
func NewShellGraphRunner(p ShellGraphRunnerParams) GraphRunner {
	return &shellGraphRunner{
		scriptPath: p.ScriptPath,
	}
}

func (s *shellGraphRunner) Compute(ctx context.Context, ws workspace.Workspace) (targethasher.Result, error) {
	return targethasher.EmptyResult(), errors.New("shellGraphRunner.Compute: not yet implemented")
}
