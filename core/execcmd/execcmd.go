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

// Package execcmd builds *exec.Cmd values that shut down child processes
// gracefully when the parent context is canceled: SIGTERM first, then SIGKILL
// after a fixed grace period.
package execcmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// gracePeriod is how long the child has to exit after SIGTERM before the Go
// runtime escalates to SIGKILL via Cmd.WaitDelay.
const gracePeriod = 10 * time.Second

// CommandContext is a drop-in replacement for exec.CommandContext that sends
// SIGTERM when ctx is canceled and escalates to SIGKILL after gracePeriod if
// the process is still running. This gives child processes (e.g. git, bazel)
// a chance to release lock files and disconnect cleanly before being killed.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Cancel = func() error {
		err := cmd.Process.Signal(syscall.SIGTERM)
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	cmd.WaitDelay = gracePeriod
	return cmd
}
