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

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimal returns a valid minimal YAML config string.
func minimal() string {
	return `
repository:
  - remote: "https://example.com/repo.git"
service:
  max_worker_pool_size: 2
  workspaces_root_path: "/tmp/tango-repo-manager"
`
}

func TestParseBytes_Defaults(t *testing.T) {
	cfg, err := ParseBytes([]byte(minimal()))
	require.NoError(t, err)

	assert.Equal(t, StorageTypeMemory, cfg.Storage.Type, "storage type should default to memory")
	assert.Equal(t, DefaultMaxMessageBytes, cfg.Service.MaxMessageBytes, "max_message_bytes should default")
	assert.Equal(t, DefaultQueryTimeoutSeconds, cfg.Repository[0].QueryTimeoutSeconds, "query_timeout_seconds should default to 600")
	require.NotNil(t, cfg.Repository[0].BzlmodEnabled)
	assert.True(t, *cfg.Repository[0].BzlmodEnabled, "bzlmod_enabled should default to true")
}

func TestParseBytes_ExplicitValues(t *testing.T) {
	yamlStr := `
repository:
  - remote: "https://example.com/repo.git"
    query_timeout_seconds: 60
    bzlmod_enabled: false
    full_hash_repos: ["//"]
    excluded_files: ["*.gen.go"]
    stream_bazel_logs: true
storage:
  type: "memory"
service:
  max_worker_pool_size: 4
  workspaces_root_path: "/custom/clone"
  max_message_bytes: 1000000
`
	cfg, err := ParseBytes([]byte(yamlStr))
	require.NoError(t, err)

	assert.Equal(t, StorageTypeMemory, cfg.Storage.Type)
	assert.Equal(t, int64(60), cfg.Repository[0].QueryTimeoutSeconds)
	require.NotNil(t, cfg.Repository[0].BzlmodEnabled)
	assert.False(t, *cfg.Repository[0].BzlmodEnabled)
	assert.Equal(t, []string{"//"}, cfg.Repository[0].FullHashRepos)
	assert.Equal(t, []string{"*.gen.go"}, cfg.Repository[0].ExcludedFiles)
	assert.True(t, cfg.Repository[0].StreamBazelLogs)
	assert.Equal(t, "/custom/clone", cfg.Service.WorkspacesRootPath)
	assert.Equal(t, 1000000, cfg.Service.MaxMessageBytes)
}

func TestParseBytes_StorageValidation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{
			name: "memory explicit",
			yaml: `
storage:
  type: "memory"
repository:
  - remote: "https://example.com/r.git"
service:
  max_worker_pool_size: 1
  workspaces_root_path: "/tmp/tango-repo-manager"
`,
		},
		{
			name: "empty defaults to memory",
			yaml: `
repository:
  - remote: "https://example.com/r.git"
service:
  max_worker_pool_size: 1
  workspaces_root_path: "/tmp/tango-repo-manager"
`,
		},
		{
			name: "disk with root_path",
			yaml: `
storage:
  type: "disk"
  disk:
    root_path: "/tmp/store"
repository:
  - remote: "https://example.com/r.git"
service:
  max_worker_pool_size: 1
  workspaces_root_path: "/tmp/tango-repo-manager"
`,
		},
		{
			name:    "disk without root_path",
			wantErr: true,
			yaml: `
storage:
  type: "disk"
repository:
  - remote: "https://example.com/r.git"
service:
  max_worker_pool_size: 1
  workspaces_root_path: "/tmp/tango-repo-manager"
`,
		},
		{
			name:    "disk with empty root_path",
			wantErr: true,
			yaml: `
storage:
  type: "disk"
  disk:
    root_path: ""
repository:
  - remote: "https://example.com/r.git"
service:
  max_worker_pool_size: 1
  workspaces_root_path: "/tmp/tango-repo-manager"
`,
		},
		{
			name:    "unknown storage type",
			wantErr: true,
			yaml: `
storage:
  type: "s3"
repository:
  - remote: "https://example.com/r.git"
service:
  max_worker_pool_size: 1
  workspaces_root_path: "/tmp/tango-repo-manager"
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseBytes([]byte(tt.yaml))
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestParseBytes_UnknownFieldsRejected(t *testing.T) {
	yamlStr := `
repository:
  - remote: "https://example.com/repo.git"
service:
  max_worker_pool_size: 1
  workspaces_root_path: "/tmp/tango-repo-manager"
  totally_bogus_field: true
`
	_, err := ParseBytes([]byte(yamlStr))
	require.Error(t, err, "unknown YAML fields should be rejected")
}

func TestParseBytes_WorkerPoolSizeRequired(t *testing.T) {
	yamlStr := `
repository:
  - remote: "https://example.com/repo.git"
service:
  max_worker_pool_size: 0
  workspaces_root_path: "/tmp/tango-repo-manager"
`
	_, err := ParseBytes([]byte(yamlStr))
	require.Error(t, err)
}

func TestParseBytes_EmptyRemoteRejected(t *testing.T) {
	yamlStr := `
repository:
  - remote: ""
service:
  max_worker_pool_size: 1
  workspaces_root_path: "/tmp/tango-repo-manager"
`
	_, err := ParseBytes([]byte(yamlStr))
	require.Error(t, err)
}

func TestParseBytes_DuplicateRemoteRejected(t *testing.T) {
	yamlStr := `
repository:
  - remote: "https://example.com/repo.git"
  - remote: "https://example.com/repo.git"
service:
  max_worker_pool_size: 1
  workspaces_root_path: "/tmp/tango-repo-manager"
`
	_, err := ParseBytes([]byte(yamlStr))
	require.Error(t, err)
}

func TestParseBytes_WorkspacesRootPathRequired(t *testing.T) {
	yamlStr := `
repository:
  - remote: "https://example.com/repo.git"
service:
  max_worker_pool_size: 1
`
	_, err := ParseBytes([]byte(yamlStr))
	require.Error(t, err)
}

func TestParse_FileNotFound(t *testing.T) {
	_, err := Parse("/nonexistent/path/config.yaml")
	require.Error(t, err)
}

func TestParse_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(minimal()), 0o644))

	cfg, err := Parse(path)
	require.NoError(t, err)
	assert.Equal(t, StorageTypeMemory, cfg.Storage.Type)
}

func TestGetRepositoryConfig(t *testing.T) {
	cfg, err := ParseBytes([]byte(minimal()))
	require.NoError(t, err)

	repo, ok := cfg.GetRepositoryConfig("https://example.com/repo.git")
	assert.True(t, ok)
	assert.Equal(t, "https://example.com/repo.git", repo.Remote)

	_, ok = cfg.GetRepositoryConfig("https://missing.com/repo.git")
	assert.False(t, ok)
}
