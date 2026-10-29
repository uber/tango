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
	"os"
	"os/exec"
	"path/filepath"
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
				Logger:        zap.NewNop(),
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
				Logger:        zap.NewNop(),
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
		Logger:        zap.NewNop(),
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

func fakeBazel(t *testing.T, script string) string {
	path := filepath.Join(t.TempDir(), "fake-bazel")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755))
	return path
}

func TestOutputBase(t *testing.T) {
	t.Run("passes startup options before the info command", func(t *testing.T) {
		bazelCmd := fakeBazel(t, `[ "$1 $2 $3 $4" = "--output_base=/custom info output_base " ] || exit 1; echo /custom`)

		got, err := OutputBase(context.Background(), t.TempDir(), bazelCmd, []string{"--output_base=/custom"})

		require.NoError(t, err)
		assert.Equal(t, "/custom", got)
	})

	t.Run("errors when bazel fails", func(t *testing.T) {
		_, err := OutputBase(context.Background(), t.TempDir(), fakeBazel(t, "exit 1"), nil)

		require.Error(t, err)
	})

	t.Run("errors on empty path", func(t *testing.T) {
		_, err := OutputBase(context.Background(), t.TempDir(), fakeBazel(t, "echo"), nil)

		require.Error(t, err)
	})
}

func TestRepoMapping(t *testing.T) {
	t.Run("parses the mapping", func(t *testing.T) {
		bazelCmd := fakeBazel(t, `[ "$1 $2 $3" = "mod dump_repo_mapping " ] && echo '{"apparent":"+ext+canonical","bazel_tools":"bazel_tools"}'`)

		got, err := RepoMapping(context.Background(), t.TempDir(), bazelCmd, nil, nil)

		require.NoError(t, err)
		assert.Equal(t, map[string]string{"apparent": "+ext+canonical", "bazel_tools": "bazel_tools"}, got)
	})

	t.Run("forwards startup options and module resolution args only", func(t *testing.T) {
		bazelCmd := fakeBazel(t, `[ "$*" = "--output_base=/custom mod --registry=https://r dump_repo_mapping " ] || exit 1; echo '{"a":"b"}'`)

		got, err := RepoMapping(context.Background(), t.TempDir(), bazelCmd,
			[]string{"--output_base=/custom"},
			[]string{"--keep_going", "--registry=https://r", "--noimplicit_deps"})

		require.NoError(t, err)
		assert.Equal(t, map[string]string{"a": "b"}, got)
	})

	t.Run("errors when bazel fails", func(t *testing.T) {
		_, err := RepoMapping(context.Background(), t.TempDir(), fakeBazel(t, "exit 1"), nil, nil)

		require.Error(t, err)
	})

	t.Run("errors on output that is not a JSON mapping", func(t *testing.T) {
		_, err := RepoMapping(context.Background(), t.TempDir(), fakeBazel(t, "echo not-json"), nil, nil)

		require.Error(t, err)
	})
}

func TestModuleResolutionArgs(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "empty", in: nil, want: nil},
		{name: "drops query-only flags", in: []string{"--keep_going", "--noimplicit_deps", "--output=proto"}, want: nil},
		{name: "keeps equals-form value flags", in: []string{"--registry=https://r", "--keep_going", "--override_module=m=/p"}, want: []string{"--registry=https://r", "--override_module=m=/p"}},
		{name: "keeps space-form value with its value", in: []string{"--registry", "https://r", "--keep_going"}, want: []string{"--registry", "https://r"}},
		{name: "does not treat a dropped flag's value as a flag", in: []string{"--unknown", "--registry=x"}, want: []string{"--registry=x"}},
		{name: "keeps inject_repository", in: []string{"--inject_repository=n=/p"}, want: []string{"--inject_repository=n=/p"}},
		{name: "keeps repository_cache, including the empty form that disables it", in: []string{"--repository_cache=/c", "--repository_cache=", "--repository_cache", "/d"}, want: []string{"--repository_cache=/c", "--repository_cache=", "--repository_cache", "/d"}},
		{name: "keeps other cache locations", in: []string{"--repo_contents_cache=/r", "--distdir", "/d", "--vendor_dir=/v", "--downloader_config=/f"}, want: []string{"--repo_contents_cache=/r", "--distdir", "/d", "--vendor_dir=/v", "--downloader_config=/f"}},
		{name: "keeps remote downloader and credential flags", in: []string{"--experimental_remote_downloader=grpc://h:1", "--credential_helper=/bin/h"}, want: []string{"--experimental_remote_downloader=grpc://h:1", "--credential_helper=/bin/h"}},
		{name: "drops config and lockfile_mode", in: []string{"--config=ci", "--lockfile_mode=error"}, want: nil},
		{name: "drops boolean flags", in: []string{"--enable_workspace", "--noignore_dev_dependency", "--enable_bzlmod", "--experimental_remote_downloader_local_fallback"}, want: nil},
		{name: "drops flags that do not change the mapping", in: []string{"--config=ci", "--lockfile_mode=error", "--repo_env=A=b"}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ModuleResolutionArgs(tt.in))
		})
	}
}
