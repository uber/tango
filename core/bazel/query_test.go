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
	"os"
	"strings"
	"testing"
	"time"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protodelim"
)

func TestExecuteQuery_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockCmd := NewMockCommander(ctrl)
	protoData := marshalTarget(t, "//pkg:target")
	mockCmd.EXPECT().Run(gomock.Any(), gomock.Any()).DoAndReturn(func(stdout, _ io.Writer) error {
		_, err := stdout.Write(protoData)
		return err
	})

	client := newTestClient(t, func(context.Context, string, ...string) Commander { return mockCmd })
	resp, err := client.ExecuteQuery(context.Background(), &QueryRequest{Query: "//..."})

	require.NoError(t, err)
	require.Len(t, resp.Result.Target, 1)
	assert.Equal(t, "//pkg:target", resp.Result.Target[0].Rule.GetName())
}

func TestExecuteQuery_WithStartupOptions(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockCmd := NewMockCommander(ctrl)
	mockCmd.EXPECT().Run(gomock.Any(), gomock.Any()).Return(nil)

	var capturedArgs []string
	client := newTestClient(t, func(_ context.Context, _ string, args ...string) Commander {
		capturedArgs = args
		return mockCmd
	})
	_, err := client.ExecuteQuery(context.Background(), &QueryRequest{
		Query:          "//...",
		StartupOptions: []string{"--bazelrc=/custom/.bazelrc", "--output_base=/tmp/bazel"},
		AdditionalArgs: []string{"--keep_going"},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{
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
	mockCmd := NewMockCommander(ctrl)
	var cmdCtx context.Context
	mockCmd.EXPECT().Run(gomock.Any(), gomock.Any()).DoAndReturn(func(io.Writer, io.Writer) error {
		<-cmdCtx.Done()
		return errors.New("signal: terminated")
	})

	client := newTestClient(t, func(ctx context.Context, _ string, _ ...string) Commander {
		cmdCtx = ctx
		return mockCmd
	})
	client.queryTimeout = 10 * time.Millisecond
	result, err := client.executeQueryInternal(context.Background(), "//...", nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, result)
}

func TestExecuteQueryInternal_PreCanceledContext(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockCmd := NewMockCommander(ctrl)
	client := newTestClient(t, func(context.Context, string, ...string) Commander { return mockCmd })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := client.executeQueryInternal(ctx, "//...", nil)

	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, result)
}

func TestExecuteQueryInternal_CancelDuringParsing(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	mockCmd := NewMockCommander(ctrl)
	ctx, cancel := context.WithCancel(context.Background())
	mockCmd.EXPECT().Run(gomock.Any(), gomock.Any()).DoAndReturn(func(stdout, _ io.Writer) error {
		_, err := stdout.Write(marshalTarget(t, "//pkg:target"))
		cancel()
		return err
	})

	client := newTestClient(t, func(context.Context, string, ...string) Commander { return mockCmd })
	result, err := client.executeQueryInternal(ctx, "//...", nil)

	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, result)
}

func TestExecuteQueryInternal_CapturesCompleteOutput(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockCmd := NewMockCommander(ctrl)
	const targetCount = 100
	const stderrTail = "FINAL STDERR LINE"
	mockCmd.EXPECT().Run(gomock.Any(), gomock.Any()).DoAndReturn(func(stdout, stderr io.Writer) error {
		for i := 0; i < targetCount; i++ {
			if _, err := stdout.Write(marshalTarget(t, fmt.Sprintf("//pkg:target%d", i))); err != nil {
				return err
			}
		}
		_, err := io.WriteString(stderr, strings.Repeat("bazel progress line\n", 200)+stderrTail)
		if err != nil {
			return err
		}
		return errors.New("exit status 7")
	})

	client := newTestClient(t, func(context.Context, string, ...string) Commander { return mockCmd })
	result, err := client.executeQueryInternal(context.Background(), "//...", nil)

	require.Error(t, err)
	require.Len(t, result.Target, targetCount)
	assert.Contains(t, err.Error(), stderrTail)
}

func TestExecuteQueryInternal_ParseFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockCmd := NewMockCommander(ctrl)
	mockCmd.EXPECT().Run(gomock.Any(), gomock.Any()).DoAndReturn(func(stdout, _ io.Writer) error {
		_, err := io.WriteString(stdout, "not a streamed proto")
		return err
	})

	client := newTestClient(t, func(context.Context, string, ...string) Commander { return mockCmd })
	result, err := client.executeQueryInternal(context.Background(), "//...", nil)

	require.Error(t, err)
	assert.Nil(t, result)
}

func TestExecuteQueryInternal_StreamLogs(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockCmd := NewMockCommander(ctrl)
	mockCmd.EXPECT().Run(gomock.Any(), os.Stderr).Return(errors.New("exit status 1"))

	client := newTestClient(t, func(context.Context, string, ...string) Commander { return mockCmd })
	client.streamLogs = true
	_, err := client.executeQueryInternal(context.Background(), "//...", nil)
	require.Error(t, err)
}

func TestExecuteQuery_ErrorCase(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockCmd := NewMockCommander(ctrl)
	mockCmd.EXPECT().Run(gomock.Any(), gomock.Any()).Return(errors.New("command failed"))

	client := newTestClient(t, func(context.Context, string, ...string) Commander { return mockCmd })
	resp, err := client.ExecuteQuery(context.Background(), &QueryRequest{Query: "//..."})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "command failed")
}

func marshalTarget(t *testing.T, name string) []byte {
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

func newTestClient(t *testing.T, execCmd func(context.Context, string, ...string) Commander) *BazelClient {
	t.Helper()
	client, err := NewBazelClient(context.Background(), Params{
		BazelCommand:       "bazel",
		WorkspacePath:      "/tmp/test",
		EnvVarsMap:         map[string]string{},
		Logger:             zap.NewNop().Sugar(),
		ExecCommandContext: execCmd,
	})
	require.NoError(t, err)
	return client
}
