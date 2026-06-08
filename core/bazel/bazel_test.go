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
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestNewBazelClient(t *testing.T) {
	tests := []struct {
		name   string
		params Params
	}{
		{
			name: "with exec command context",
			params: Params{
				BazelCommand:  "bazel",
				WorkspacePath: "/tmp/test",
				EnvVarsMap:    map[string]string{"FOO": "bar"},
				Logger:        zap.NewNop().Sugar(),
				ExecCommandContext: func(ctx context.Context, name string, arg ...string) commander {
					return nil
				},
			},
		},
		{
			name: "without exec command context",
			params: Params{
				BazelCommand:  "bazel",
				WorkspacePath: "/tmp/test",
				EnvVarsMap:    map[string]string{"FOO": "bar"},
				Logger:        zap.NewNop().Sugar(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewBazelClient(context.Background(), tt.params)
			require.NoError(t, err)
			require.NotNil(t, client)
			assert.Equal(t, tt.params.BazelCommand, client.bazelCommand)
			assert.Equal(t, tt.params.WorkspacePath, client.workspacePath)
			assert.Equal(t, tt.params.EnvVarsMap, client.envVarsMap)
			assert.Equal(t, tt.params.Logger, client.logger)
			assert.NotNil(t, client.execCommandContext)
		})
	}
}

func TestNewBazelClient_WithNilExecCommand(t *testing.T) {
	client, err := NewBazelClient(context.Background(), Params{
		BazelCommand:  "bazel",
		WorkspacePath: "/workspace",
		EnvVarsMap:    map[string]string{"KEY": "value"},
		Logger:        zap.NewNop().Sugar(),
	})
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, client.execCommandContext)

	cmd := client.execCommandContext(context.Background(), "test", "arg1")
	require.NotNil(t, cmd)
	execCmd, ok := cmd.(*exec.Cmd)
	require.True(t, ok)
	assert.Equal(t, "/workspace", execCmd.Dir)
	assert.Contains(t, execCmd.Env, "KEY=value")
}
