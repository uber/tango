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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/bazel/commandermock"
	"go.uber.org/goleak"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protodelim"
)

func TestExecuteQuery_Success(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	mockCmd := commandermock.NewMockcommander(ctrl)
	var (
		ruleName, ruleClass = "//pkg:target", "go_library"
	)
	target := &buildpb.Target{
		Type: buildpb.Target_RULE.Enum(),
		Rule: &buildpb.Rule{
			Name:      &ruleName,
			RuleClass: &ruleClass,
		},
	}
	var protoData bytes.Buffer
	_, err := protodelim.MarshalTo(&protoData, target)
	require.NoError(t, err)
	gomock.InOrder(
		mockCmd.EXPECT().StdoutPipe().Return(io.NopCloser(&protoData), nil),
		mockCmd.EXPECT().StderrPipe().Return(io.NopCloser(strings.NewReader("")), nil),
		mockCmd.EXPECT().Start().Return(nil),
		mockCmd.EXPECT().Wait().Return(nil),
	)
	client, err := NewBazelClient(context.Background(), Params{
		BazelCommand:  "bazel",
		WorkspacePath: "/tmp/test",
		EnvVarsMap:    map[string]string{},
		Logger:        zap.NewNop().Sugar(),
		ExecCommandContext: func(ctx context.Context, name string, arg ...string) commander {
			return mockCmd
		},
	})

	resp, err := client.ExecuteQuery(context.Background(), &QueryRequest{Query: "//..."})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Result)
	require.Equal(t, 1, len(resp.Result.Target))
	assert.Equal(t, &ruleName, resp.Result.Target[0].Rule.Name)
	assert.Equal(t, &ruleClass, resp.Result.Target[0].Rule.RuleClass)
}

func TestExecuteQuery_WithStartupOptions(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	mockCmd := commandermock.NewMockcommander(ctrl)
	var (
		ruleName, ruleClass = "//pkg:target", "go_library"
	)
	target := &buildpb.Target{
		Type: buildpb.Target_RULE.Enum(),
		Rule: &buildpb.Rule{
			Name:      &ruleName,
			RuleClass: &ruleClass,
		},
	}
	var protoData bytes.Buffer
	_, err := protodelim.MarshalTo(&protoData, target)
	require.NoError(t, err)

	var capturedArgs []string
	gomock.InOrder(
		mockCmd.EXPECT().StdoutPipe().Return(io.NopCloser(&protoData), nil),
		mockCmd.EXPECT().StderrPipe().Return(io.NopCloser(strings.NewReader("")), nil),
		mockCmd.EXPECT().Start().Return(nil),
		mockCmd.EXPECT().Wait().Return(nil),
	)
	client, err := NewBazelClient(context.Background(), Params{
		BazelCommand:  "bazel",
		WorkspacePath: "/tmp/test",
		EnvVarsMap:    map[string]string{},
		Logger:        zap.NewNop().Sugar(),
		ExecCommandContext: func(ctx context.Context, name string, arg ...string) commander {
			capturedArgs = arg
			return mockCmd
		},
	})
	require.NoError(t, err)
	resp, err := client.ExecuteQuery(context.Background(), &QueryRequest{
		Query:          "//...",
		StartupOptions: []string{"--bazelrc=/custom/.bazelrc", "--output_base=/tmp/bazel"},
		AdditionalArgs: []string{"--keep_going"},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify command structure: bazel <startupOpts> query <AdditionalArgs> --output=streamed_proto <Query>
	require.Equal(t, []string{
		"--bazelrc=/custom/.bazelrc",
		"--output_base=/tmp/bazel",
		"query",
		"--keep_going",
		"--output=streamed_proto",
		"//...",
	}, capturedArgs)
}

func TestExecuteQueryInternal_ContextTimeout(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	mockCmd := commandermock.NewMockcommander(ctrl)

	prStdout, pwStdout := io.Pipe()
	prStderr, pwStderr := io.Pipe()

	// Set up the mock expectations in the exact order they will be called.
	gomock.InOrder(
		mockCmd.EXPECT().StdoutPipe().Return(prStdout, nil),
		mockCmd.EXPECT().StderrPipe().Return(prStderr, nil),
		mockCmd.EXPECT().Start().Return(nil),
		mockCmd.EXPECT().Wait().DoAndReturn(func() error {
			// Simulate process ending after timeout
			return context.DeadlineExceeded
		}),
	)

	client, err := NewBazelClient(context.Background(), Params{
		BazelCommand:  "bazel",
		WorkspacePath: "/tmp/test",
		Logger:        zap.NewNop().Sugar(),
		EnvVarsMap:    map[string]string{},
		QueryTimeout:  10 * time.Millisecond, // Short timeout for test

		ExecCommandContext: func(ctx context.Context, name string, arg ...string) commander {
			// Simulate process behavior: when context is cancelled, close pipes
			go func() {
				<-ctx.Done()
				// Close pipes to unblock readers
				pwStdout.Close()
				pwStderr.Close()
			}()
			return mockCmd
		},
	})
	require.NoError(t, err)
	result, err := client.executeQueryInternal(context.Background(), "//...", nil)
	require.Nil(t, result)
	require.Error(t, err)
	// Should get timeout or deadline exceeded error
	assert.Contains(t, err.Error(), "deadline exceeded")
}

// TestExecuteQueryInternal_PipesHeldOpenAfterCancel simulates a descendant of
// the killed bazel process inheriting the stdout/stderr pipes and holding
// their write ends open past the kill (go.dev/issue/23019). Since cmd.Wait —
// which would force-close the parent ends — only runs after the stream reads
// finish, the pipe watchdog must unblock the reads or the query would hang.
func TestExecuteQueryInternal_PipesHeldOpenAfterCancel(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	mockCmd := commandermock.NewMockcommander(ctrl)

	// The write ends are intentionally never closed by the "process".
	prStdout, pwStdout := io.Pipe()
	prStderr, pwStderr := io.Pipe()
	defer pwStdout.Close()
	defer pwStderr.Close()

	var cmdCtx context.Context
	gomock.InOrder(
		mockCmd.EXPECT().StdoutPipe().Return(prStdout, nil),
		mockCmd.EXPECT().StderrPipe().Return(prStderr, nil),
		mockCmd.EXPECT().Start().Return(nil),
		mockCmd.EXPECT().Wait().DoAndReturn(func() error {
			// The process itself dies with the context; only its orphaned
			// descendant lives on, holding the pipes.
			<-cmdCtx.Done()
			return context.DeadlineExceeded
		}),
	)

	client, err := NewBazelClient(context.Background(), Params{
		BazelCommand:  "bazel",
		WorkspacePath: "/tmp/test",
		EnvVarsMap:    map[string]string{},
		Logger:        zap.NewNop().Sugar(),
		QueryTimeout:  10 * time.Millisecond,
		ExecCommandContext: func(ctx context.Context, name string, arg ...string) commander {
			cmdCtx = ctx
			return mockCmd
		},
	})
	require.NoError(t, err)
	client.pipeUnblockDelay = 20 * time.Millisecond

	type queryOutcome struct {
		result *buildpb.QueryResult
		err    error
	}
	done := make(chan queryOutcome, 1)
	go func() {
		result, err := client.executeQueryInternal(context.Background(), "//...", nil)
		done <- queryOutcome{result: result, err: err}
	}()

	select {
	case out := <-done:
		require.Error(t, out.err)
		assert.ErrorIs(t, out.err, context.DeadlineExceeded)
		assert.Nil(t, out.result)
	case <-time.After(10 * time.Second):
		t.Fatal("executeQueryInternal never returned: pipe reads were not unblocked")
	}
}

// TestExecuteQueryInternal_DrainsStreamsBeforeWait verifies that cmd.Wait runs
// only after the stream reads finish. The mock Waits emulate exec.Cmd.Wait,
// which force-closes the parent pipe ends when it returns — had Wait run
// before the reads completed, the still-streaming output would be truncated.
func TestExecuteQueryInternal_DrainsStreamsBeforeWait(t *testing.T) {
	t.Run("stdout is fully parsed", func(t *testing.T) {
		defer goleak.VerifyNone(t)
		ctrl := gomock.NewController(t)
		mockCmd := commandermock.NewMockcommander(ctrl)

		prStdout, pwStdout := io.Pipe()
		prStderr, pwStderr := io.Pipe()

		// io.Pipe is synchronous, so every write below blocks until the query's
		// stream reader consumes it: the "process" is still producing output
		// while the query runs.
		const targetCount = 100
		go func() {
			defer pwStdout.Close()
			defer pwStderr.Close()
			ruleClass := "go_library"
			for i := 0; i < targetCount; i++ {
				name := fmt.Sprintf("//pkg:target%d", i)
				target := &buildpb.Target{
					Type: buildpb.Target_RULE.Enum(),
					Rule: &buildpb.Rule{Name: &name, RuleClass: &ruleClass},
				}
				if _, err := protodelim.MarshalTo(pwStdout, target); err != nil {
					return
				}
			}
		}()

		gomock.InOrder(
			mockCmd.EXPECT().StdoutPipe().Return(prStdout, nil),
			mockCmd.EXPECT().StderrPipe().Return(prStderr, nil),
			mockCmd.EXPECT().Start().Return(nil),
			mockCmd.EXPECT().Wait().DoAndReturn(func() error {
				prStdout.Close()
				prStderr.Close()
				return nil
			}),
		)

		client, err := NewBazelClient(context.Background(), Params{
			BazelCommand:  "bazel",
			WorkspacePath: "/tmp/test",
			EnvVarsMap:    map[string]string{},
			Logger:        zap.NewNop().Sugar(),
			ExecCommandContext: func(ctx context.Context, name string, arg ...string) commander {
				return mockCmd
			},
		})
		require.NoError(t, err)

		result, err := client.executeQueryInternal(context.Background(), "//...", nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Len(t, result.Target, targetCount)
	})

	t.Run("stderr is fully captured in the failure error", func(t *testing.T) {
		defer goleak.VerifyNone(t)
		ctrl := gomock.NewController(t)
		mockCmd := commandermock.NewMockcommander(ctrl)

		prStderr, pwStderr := io.Pipe()

		const stderrTail = "FINAL STDERR LINE"
		go func() {
			defer pwStderr.Close()
			_, _ = io.WriteString(pwStderr, strings.Repeat("bazel progress line\n", 200)+stderrTail)
		}()

		gomock.InOrder(
			mockCmd.EXPECT().StdoutPipe().Return(io.NopCloser(strings.NewReader("")), nil),
			mockCmd.EXPECT().StderrPipe().Return(prStderr, nil),
			mockCmd.EXPECT().Start().Return(nil),
			mockCmd.EXPECT().Wait().DoAndReturn(func() error {
				prStderr.Close()
				return errors.New("exit status 7")
			}),
		)

		client, err := NewBazelClient(context.Background(), Params{
			BazelCommand:  "bazel",
			WorkspacePath: "/tmp/test",
			EnvVarsMap:    map[string]string{},
			Logger:        zap.NewNop().Sugar(),
			ExecCommandContext: func(ctx context.Context, name string, arg ...string) commander {
				return mockCmd
			},
		})
		require.NoError(t, err)

		result, err := client.executeQueryInternal(context.Background(), "//...", nil)
		require.Error(t, err)
		require.NotNil(t, result)
		// This asserts on the payload the error carries (the captured stderr),
		// not on the error's own wording: the tail marker only appears if the
		// whole stream was drained before the process was reaped.
		assert.Contains(t, err.Error(), stderrTail)
	})
}

func TestExecuteQueryInternal_Failures(t *testing.T) {
	tests := []struct {
		name            string
		setupMock       func(*commandermock.Mockcommander)
		expectedError   string
		expectNilResult bool
	}{
		{
			name: "stdout pipe failure",
			setupMock: func(m *commandermock.Mockcommander) {
				m.EXPECT().StdoutPipe().Return(nil, errors.New("stdout pipe failed"))
			},
			expectedError:   "stdout pipe failed",
			expectNilResult: true,
		},
		{
			name: "stderr pipe failure",
			setupMock: func(m *commandermock.Mockcommander) {
				m.EXPECT().StdoutPipe().Return(io.NopCloser(strings.NewReader("")), nil)
				m.EXPECT().StderrPipe().Return(nil, errors.New("stderr pipe failed"))
			},
			expectedError:   "stderr pipe failed",
			expectNilResult: true,
		},
		{
			name: "command start failure",
			setupMock: func(m *commandermock.Mockcommander) {
				m.EXPECT().StdoutPipe().Return(io.NopCloser(strings.NewReader("")), nil)
				m.EXPECT().StderrPipe().Return(io.NopCloser(strings.NewReader("")), nil)
				m.EXPECT().Start().Return(errors.New("failed to start process"))
			},
			expectedError:   "failed to start process",
			expectNilResult: true,
		},
		{
			name: "command wait failure",
			setupMock: func(m *commandermock.Mockcommander) {
				m.EXPECT().StdoutPipe().Return(io.NopCloser(strings.NewReader("")), nil)
				m.EXPECT().StderrPipe().Return(io.NopCloser(strings.NewReader("")), nil)
				m.EXPECT().Start().Return(nil)
				m.EXPECT().Wait().Return(errors.New("command wait failed"))
			},
			expectedError:   "command wait failed",
			expectNilResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer goleak.VerifyNone(t)
			ctrl := gomock.NewController(t)
			mockCmd := commandermock.NewMockcommander(ctrl)
			tt.setupMock(mockCmd)

			client, err := NewBazelClient(context.Background(), Params{
				BazelCommand:  "bazel",
				WorkspacePath: "/tmp/test",
				EnvVarsMap:    map[string]string{},
				Logger:        zap.NewNop().Sugar(),
				ExecCommandContext: func(ctx context.Context, name string, arg ...string) commander {
					return mockCmd
				},
			})
			require.NoError(t, err)
			result, err := client.executeQueryInternal(context.Background(), "//...", nil)
			require.Error(t, err)
			if tt.expectNilResult {
				require.Nil(t, result)
			} else {
				require.NotNil(t, result)
			}
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

func TestExecuteQuery_ErrorCase(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	mockCmd := commandermock.NewMockcommander(ctrl)

	mockCmd.EXPECT().StdoutPipe().Return(nil, errors.New("stdout pipe failed"))

	client, err := NewBazelClient(context.Background(), Params{
		BazelCommand:  "bazel",
		WorkspacePath: "/tmp/test",
		EnvVarsMap:    map[string]string{},
		Logger:        zap.NewNop().Sugar(),
		ExecCommandContext: func(ctx context.Context, name string, arg ...string) commander {
			return mockCmd
		},
	})

	resp, err := client.ExecuteQuery(context.Background(), &QueryRequest{Query: "//..."})
	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "stdout pipe failed")
}
