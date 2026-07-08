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

package bazel

import (
	"io"
	"os/exec"
)

type commander interface {
	Run(stdout, stderr io.Writer) error
}

type execCommander struct {
	*exec.Cmd
}

// Run attaches the supplied writers before starting the command. Query
// execution intentionally supplies *os.File values: os/exec passes those file
// descriptors directly to the child instead of creating copy goroutines that
// Wait must drain.
func (c *execCommander) Run(stdout, stderr io.Writer) error {
	c.Stdout = stdout
	c.Stderr = stderr
	return c.Cmd.Run()
}
