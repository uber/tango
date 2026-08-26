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
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/bazel/commandermock"
	"github.com/uber/tango/core/execcmd"
	"go.uber.org/goleak"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protodelim"
)

func TestExecuteQuery_Success(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	mockCmd := commandermock.NewMockcommander(ctrl)
	protoData := marshalQueryTarget(t, "//pkg:target")
	var stdout io.Writer
	gomock.InOrder(
		mockCmd.EXPECT().Start(gomock.Any(), gomock.Any()).DoAndReturn(func(out, _ io.Writer) error {
			stdout = out
			return nil
		}),
		mockCmd.EXPECT().Wait().DoAndReturn(func() error {
			_, err := stdout.Write(protoData)
			return err
		}),
	)
	client := newQueryTestClient(t, func(context.Context, string, ...string) commander {
		return mockCmd
	})

	resp, err := client.ExecuteQuery(context.Background(), &QueryRequest{Query: "//..."})

	require.NoError(t, err)
	require.Len(t, resp.Result.Target, 1)
	assert.Equal(t, "//pkg:target", resp.Result.Target[0].Rule.GetName())
	assert.Equal(t, "go_library", resp.Result.Target[0].Rule.GetRuleClass())
}

func TestExecuteQuery_WithStartupOptions(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	mockCmd := commandermock.NewMockcommander(ctrl)
	gomock.InOrder(
		mockCmd.EXPECT().Start(gomock.Any(), gomock.Any()).Return(nil),
		mockCmd.EXPECT().Wait().Return(nil),
	)
	var capturedArgs []string
	client := newQueryTestClient(t, func(_ context.Context, _ string, args ...string) commander {
		capturedArgs = args
		return mockCmd
	})

	resp, err := client.ExecuteQuery(context.Background(), &QueryRequest{
		Query:          "//...",
		StartupOptions: []string{"--bazelrc=/custom/.bazelrc", "--output_base=/tmp/bazel"},
		AdditionalArgs: []string{"--keep_going"},
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, []string{
		"--bazelrc=/custom/.bazelrc",
		"--output_base=/tmp/bazel",
		"query",
		"--keep_going",
		"--output=streamed_proto",
		"//...",
	}, capturedArgs)
}

func TestExecuteQueryInternal_WaitRunsWhileStreamsDrain(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	mockCmd := commandermock.NewMockcommander(ctrl)
	protoData := marshalQueryTarget(t, "//pkg:target")
	var stdout io.Writer
	gomock.InOrder(
		mockCmd.EXPECT().Start(gomock.Any(), gomock.Any()).DoAndReturn(func(out, _ io.Writer) error {
			stdout = out
			return nil
		}),
		mockCmd.EXPECT().Wait().DoAndReturn(func() error {
			// io.Pipe writes complete only while the reader is active. This would
			// deadlock if query execution waited for stream EOF before calling Wait.
			_, err := stdout.Write(protoData)
			return err
		}),
	)
	client := newQueryTestClient(t, func(context.Context, string, ...string) commander {
		return mockCmd
	})
	type outcome struct {
		result *buildpb.QueryResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := client.executeQueryInternal(context.Background(), "//...", nil)
		done <- outcome{result: result, err: err}
	}()

	select {
	case out := <-done:
		require.NoError(t, out.err)
		require.Len(t, out.result.Target, 1)
	case <-time.After(5 * time.Second):
		t.Fatal("query did not call Wait while its streams were draining")
	}
}

func TestExecuteQueryInternal_DescriptorRetentionDoesNotDeadlock(t *testing.T) {
	defer goleak.VerifyNone(t)
	readyReader, readyWriter, err := os.Pipe()
	require.NoError(t, err)
	releaseReader, releaseWriter, err := os.Pipe()
	require.NoError(t, err)
	doneReader, doneWriter, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = readyReader.Close()
		_ = readyWriter.Close()
		_ = releaseReader.Close()
		_ = releaseWriter.Close()
		_ = doneReader.Close()
		_ = doneWriter.Close()
	})

	client := newQueryTestClient(t, func(ctx context.Context, _ string, _ ...string) commander {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBazelSubprocessHelper$")
		cmd.Env = bazelHelperEnvironment("spawn-descriptor-holder")
		cmd.ExtraFiles = []*os.File{readyWriter, releaseReader, doneWriter}
		cmd.WaitDelay = 25 * time.Millisecond
		return &execCommander{Cmd: cmd}
	})
	type outcome struct {
		result *buildpb.QueryResult
		err    error
	}
	queryDone := make(chan outcome, 1)
	go func() {
		result, err := client.executeQueryInternal(context.Background(), "//...", nil)
		queryDone <- outcome{result: result, err: err}
	}()

	require.NoError(t, readyReader.SetReadDeadline(time.Now().Add(5*time.Second)))
	pidLine, err := bufio.NewReader(readyReader).ReadString('\n')
	require.NoError(t, err)
	holderPID, err := strconv.Atoi(strings.TrimSpace(pidLine))
	require.NoError(t, err)
	holder, err := os.FindProcess(holderPID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Kill() })

	select {
	case out := <-queryDone:
		require.Error(t, out.err)
		assert.ErrorIs(t, out.err, exec.ErrWaitDelay)
		require.NotNil(t, out.result)
	case <-time.After(5 * time.Second):
		t.Fatal("query remained blocked on descriptors retained by a descendant")
	}

	// Let the orphaned descriptor holder exit and confirm it observed release.
	require.NoError(t, releaseWriter.Close())
	require.NoError(t, doneReader.SetReadDeadline(time.Now().Add(5*time.Second)))
	var released [1]byte
	_, err = io.ReadFull(doneReader, released[:])
	require.NoError(t, err)
}

func TestExecuteQueryInternal_ContextCancellationTerminatesProcess(t *testing.T) {
	defer goleak.VerifyNone(t)
	readyReader, readyWriter, err := os.Pipe()
	require.NoError(t, err)
	releaseReader, releaseWriter, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = readyReader.Close()
		_ = readyWriter.Close()
		_ = releaseReader.Close()
		_ = releaseWriter.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newQueryTestClient(t, func(cmdCtx context.Context, _ string, _ ...string) commander {
		cmd := execcmd.CommandContext(cmdCtx, os.Args[0], "-test.run=^TestBazelSubprocessHelper$")
		cmd.Env = bazelHelperEnvironment("wait-for-cancel")
		cmd.ExtraFiles = []*os.File{readyWriter, releaseReader}
		cmd.WaitDelay = 25 * time.Millisecond
		return &execCommander{Cmd: cmd}
	})
	type outcome struct {
		result *buildpb.QueryResult
		err    error
	}
	queryDone := make(chan outcome, 1)
	go func() {
		result, err := client.executeQueryInternal(ctx, "//...", nil)
		queryDone <- outcome{result: result, err: err}
	}()

	require.NoError(t, readyReader.SetReadDeadline(time.Now().Add(5*time.Second)))
	var ready [1]byte
	_, err = io.ReadFull(readyReader, ready[:])
	require.NoError(t, err)
	cancel()

	select {
	case out := <-queryDone:
		require.Error(t, out.err)
		assert.ErrorIs(t, out.err, context.Canceled)
		assert.Nil(t, out.result)
	case <-time.After(5 * time.Second):
		t.Fatal("query process did not terminate after cancellation")
	}
}

func TestExecuteQueryInternal_ContextTimeout(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	mockCmd := commandermock.NewMockcommander(ctrl)
	var cmdCtx context.Context
	gomock.InOrder(
		mockCmd.EXPECT().Start(gomock.Any(), gomock.Any()).Return(nil),
		mockCmd.EXPECT().Wait().DoAndReturn(func() error {
			<-cmdCtx.Done()
			return context.DeadlineExceeded
		}),
	)
	client := newQueryTestClient(t, func(ctx context.Context, _ string, _ ...string) commander {
		cmdCtx = ctx
		return mockCmd
	})
	client.queryTimeout = 10 * time.Millisecond

	result, err := client.executeQueryInternal(context.Background(), "//...", nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, result)
}

func TestExecuteQueryInternal_StreamTimeoutWithoutWaitError(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	mockCmd := commandermock.NewMockcommander(ctrl)
	var cmdCtx context.Context
	gomock.InOrder(
		mockCmd.EXPECT().Start(gomock.Any(), gomock.Any()).Return(nil),
		mockCmd.EXPECT().Wait().DoAndReturn(func() error {
			<-cmdCtx.Done()
			return nil
		}),
	)
	client := newQueryTestClient(t, func(ctx context.Context, _ string, _ ...string) commander {
		cmdCtx = ctx
		return mockCmd
	})
	client.queryTimeout = 10 * time.Millisecond

	result, err := client.executeQueryInternal(context.Background(), "//...", nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, result)
}

func TestExecuteQueryInternal_WaitFailureWithoutTimeout(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	mockCmd := commandermock.NewMockcommander(ctrl)
	gomock.InOrder(
		mockCmd.EXPECT().Start(gomock.Any(), gomock.Any()).Return(nil),
		mockCmd.EXPECT().Wait().Return(errors.New("exit status 1")),
	)
	client := newQueryTestClient(t, func(context.Context, string, ...string) commander {
		return mockCmd
	})

	result, err := client.executeQueryInternal(context.Background(), "//...", nil)

	require.Error(t, err)
	require.NotNil(t, result)
	assert.NotErrorIs(t, err, context.DeadlineExceeded)
}

func TestExecuteQueryInternal_Failures(t *testing.T) {
	tests := []struct {
		name            string
		setupMock       func(*commandermock.Mockcommander)
		expectNilResult bool
	}{
		{
			name: "command start failure",
			setupMock: func(m *commandermock.Mockcommander) {
				m.EXPECT().Start(gomock.Any(), gomock.Any()).Return(errors.New("failed to start process"))
			},
			expectNilResult: true,
		},
		{
			name: "command wait failure",
			setupMock: func(m *commandermock.Mockcommander) {
				gomock.InOrder(
					m.EXPECT().Start(gomock.Any(), gomock.Any()).Return(nil),
					m.EXPECT().Wait().Return(errors.New("command wait failed")),
				)
			},
			expectNilResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer goleak.VerifyNone(t)
			ctrl := gomock.NewController(t)
			mockCmd := commandermock.NewMockcommander(ctrl)
			tt.setupMock(mockCmd)
			client := newQueryTestClient(t, func(context.Context, string, ...string) commander {
				return mockCmd
			})

			result, err := client.executeQueryInternal(context.Background(), "//...", nil)

			require.Error(t, err)
			if tt.expectNilResult {
				assert.Nil(t, result)
			} else {
				assert.NotNil(t, result)
			}
		})
	}
}

func TestExecuteQuery_ErrorCase(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	mockCmd := commandermock.NewMockcommander(ctrl)
	mockCmd.EXPECT().Start(gomock.Any(), gomock.Any()).Return(errors.New("failed to start process"))
	client := newQueryTestClient(t, func(context.Context, string, ...string) commander {
		return mockCmd
	})

	resp, err := client.ExecuteQuery(context.Background(), &QueryRequest{Query: "//..."})

	require.Error(t, err)
	assert.Nil(t, resp)
}

func marshalQueryTarget(t *testing.T, name string) []byte {
	t.Helper()
	ruleClass := "go_library"
	target := &buildpb.Target{
		Type: buildpb.Target_RULE.Enum(),
		Rule: &buildpb.Rule{Name: &name, RuleClass: &ruleClass},
	}
	var out bytes.Buffer
	_, err := protodelim.MarshalTo(&out, target)
	require.NoError(t, err)
	return out.Bytes()
}

func newQueryTestClient(t *testing.T, execCmd func(context.Context, string, ...string) commander) *BazelClient {
	t.Helper()
	client, err := NewBazelClient(context.Background(), Params{
		BazelCommand:       "bazel",
		WorkspacePath:      "/tmp/test",
		EnvVarsMap:         map[string]string{},
		Logger:             zap.NewNop(),
		ExecCommandContext: execCmd,
	})
	require.NoError(t, err)
	return client
}

const bazelHelperEnvKey = "TANGO_BAZEL_SUBPROCESS_HELPER"

func bazelHelperEnvironment(action string) []string {
	prefix := bazelHelperEnvKey + "="
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, prefix) {
			env = append(env, entry)
		}
	}
	return append(env, prefix+action)
}

func TestBazelSubprocessHelper(t *testing.T) {
	switch os.Getenv(bazelHelperEnvKey) {
	case "":
		return
	case "spawn-descriptor-holder":
		ready := os.NewFile(3, "ready")
		release := os.NewFile(4, "release")
		done := os.NewFile(5, "done")
		child := exec.Command(os.Args[0], "-test.run=^TestBazelSubprocessHelper$")
		child.Env = bazelHelperEnvironment("hold-descriptors")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		child.ExtraFiles = []*os.File{release, done}
		if err := child.Start(); err != nil {
			_, _ = fmt.Fprintf(ready, "start error: %v\n", err)
			os.Exit(2)
		}
		_, _ = fmt.Fprintf(ready, "%d\n", child.Process.Pid)
		_ = ready.Close()
		os.Exit(0)
	case "hold-descriptors":
		release := os.NewFile(3, "release")
		done := os.NewFile(4, "done")
		_, _ = io.Copy(io.Discard, release)
		_, _ = done.Write([]byte{1})
		_ = done.Close()
		os.Exit(0)
	case "wait-for-cancel":
		ready := os.NewFile(3, "ready")
		release := os.NewFile(4, "release")
		_, _ = ready.Write([]byte{1})
		_ = ready.Close()
		_, _ = io.Copy(io.Discard, release)
		os.Exit(0)
	default:
		os.Exit(2)
	}
}
