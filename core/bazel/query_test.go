package bazel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/uber/tango/core/bazel/commandermock"
	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
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

	// Create a QueryResult with the target (batch proto format, not protodelim streaming)
	queryResult := &buildpb.QueryResult{
		Target: []*buildpb.Target{target},
	}
	protoData, err := proto.Marshal(queryResult)
	require.NoError(t, err)

	gomock.InOrder(
		mockCmd.EXPECT().StdoutPipe().Return(io.NopCloser(bytes.NewReader(protoData)), nil),
		mockCmd.EXPECT().Start().Return(nil),
		mockCmd.EXPECT().Wait().Return(nil),
	)
	client, err := NewBazelClient(Params{
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

	// Create a QueryResult with the target (batch proto format)
	queryResult := &buildpb.QueryResult{
		Target: []*buildpb.Target{target},
	}
	protoData, err := proto.Marshal(queryResult)
	require.NoError(t, err)

	var capturedArgs []string
	gomock.InOrder(
		mockCmd.EXPECT().StdoutPipe().Return(io.NopCloser(bytes.NewReader(protoData)), nil),
		mockCmd.EXPECT().Start().Return(nil),
		mockCmd.EXPECT().Wait().Return(nil),
	)
	client, err := NewBazelClient(Params{
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

	// Verify command structure: bazel <startupOpts> query <AdditionalArgs> --output=proto <Query>
	require.Equal(t, []string{
		"--bazelrc=/custom/.bazelrc",
		"--output_base=/tmp/bazel",
		"query",
		"--keep_going",
		"--output=proto",
		"//...",
	}, capturedArgs)
}

func TestExecuteQueryInternal_ContextTimeout(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	mockCmd := commandermock.NewMockcommander(ctrl)

	prStdout, pwStdout := io.Pipe()

	// Set up the mock expectations in the exact order they will be called.
	// Note: When io.ReadAll gets an error (like context deadline), we return immediately
	// without calling Wait(), so we don't expect Wait() to be called here.
	gomock.InOrder(
		mockCmd.EXPECT().StdoutPipe().Return(prStdout, nil),
		mockCmd.EXPECT().Start().Return(nil),
	)

	client, err := NewBazelClient(Params{
		BazelCommand:  "bazel",
		WorkspacePath: "/tmp/test",
		Logger:        zap.NewNop().Sugar(),
		EnvVarsMap:    map[string]string{},
		QueryTimeout:  1 * time.Nanosecond, // Induce timeout immediately

		ExecCommandContext: func(ctx context.Context, name string, arg ...string) commander {
			// This goroutine simulates the OS/exec.Cmd behavior:
			//    When the context is canceled, the process is "killed",
			//    which closes its stdout/stderr pipes.
			go func() {
				<-ctx.Done() // Wait for the timeout to fire

				// "Killing" the process: close the pipe.
				//    This unblocks the Read() call in io.ReadAll.
				//    We close with the context's error so the read sees it.
				pwStdout.CloseWithError(ctx.Err())
			}()
			return mockCmd
		},
	})
	require.NoError(t, err)
	result, err := client.executeQueryInternal(context.Background(), "//...", nil)
	require.Nil(t, result)
	assert.Contains(t, err.Error(), "context deadline exceeded")
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
			name: "command start failure",
			setupMock: func(m *commandermock.Mockcommander) {
				m.EXPECT().StdoutPipe().Return(io.NopCloser(strings.NewReader("")), nil)
				m.EXPECT().Start().Return(errors.New("failed to start process"))
			},
			expectedError:   "failed to start process",
			expectNilResult: true,
		},
		{
			name: "command wait failure",
			setupMock: func(m *commandermock.Mockcommander) {
				m.EXPECT().StdoutPipe().Return(io.NopCloser(strings.NewReader("")), nil)
				m.EXPECT().Start().Return(nil)
				m.EXPECT().Wait().Return(errors.New("command wait failed"))
			},
			expectedError:   "command wait failed",
			expectNilResult: true, // Changed from false - implementation returns nil on Wait() error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer goleak.VerifyNone(t)
			ctrl := gomock.NewController(t)
			mockCmd := commandermock.NewMockcommander(ctrl)
			tt.setupMock(mockCmd)

			client, err := NewBazelClient(Params{
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

	client, err := NewBazelClient(Params{
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
