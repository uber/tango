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
	"context"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/execcmd"
	"go.uber.org/goleak"
)

func TestExecCommander_OutputFilesDoNotWaitForDescendant(t *testing.T) {
	defer goleak.VerifyNone(t)
	stdout, err := os.CreateTemp(t.TempDir(), "stdout-*")
	require.NoError(t, err)
	defer stdout.Close()
	stderr, err := os.CreateTemp(t.TempDir(), "stderr-*")
	require.NoError(t, err)
	defer stderr.Close()

	cmd := &execCommander{Cmd: exec.Command("/bin/sh", "-c", "sleep 5 & echo $! >&2")}
	started := time.Now()
	require.NoError(t, cmd.Run(stdout, stderr))
	elapsed := time.Since(started)

	contents, err := os.ReadFile(stderr.Name())
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
	require.NoError(t, err)
	process, err := os.FindProcess(pid)
	require.NoError(t, err)
	_ = process.Kill()
	require.Less(t, elapsed, 2*time.Second)
}

func TestExecCommander_ContextCancellation(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readyReader, readyWriter, err := os.Pipe()
	require.NoError(t, err)
	defer readyReader.Close()
	defer readyWriter.Close()

	cmd := execcmd.CommandContext(ctx, os.Args[0], "-test.run=TestBazelCommandHelper")
	cmd.Env = append(os.Environ(), "TANGO_BAZEL_COMMAND_HELPER=1")
	cmd.ExtraFiles = []*os.File{readyWriter}
	runner := &execCommander{Cmd: cmd}

	done := make(chan error, 1)
	go func() {
		done <- runner.Run(io.Discard, io.Discard)
	}()
	ready := make([]byte, 1)
	_, err = io.ReadFull(readyReader, ready)
	require.NoError(t, err)

	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("command did not exit after context cancellation")
	}
}

func TestBazelCommandHelper(t *testing.T) {
	if os.Getenv("TANGO_BAZEL_COMMAND_HELPER") != "1" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)

	ready := os.NewFile(3, "ready")
	_, _ = ready.Write([]byte{1})
	_ = ready.Close()

	<-signals
}
