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
  worker_pool_size: 2
`
}

func TestParseBytes_Defaults(t *testing.T) {
	cfg, err := ParseBytes([]byte(minimal()))
	require.NoError(t, err)

	assert.Equal(t, StorageTypeMemory, cfg.Storage.Type, "storage type should default to memory")
	assert.Equal(t, DefaultMaxMessageBytes, cfg.Service.MaxMessageBytes, "max_message_bytes should default")
	assert.Contains(t, cfg.Service.RepoManagerClonePath, "tango-repo-manager", "clone path should default to temp dir")
	assert.Contains(t, cfg.Service.WorkerRootPath, ".workers", "worker root should default under clone path")
	assert.Equal(t, DefaultQueryTimeoutSeconds, cfg.Repository[0].QueryTimeout, "query_timeout should default to 900")
}

func TestParseBytes_ExplicitValues(t *testing.T) {
	yamlStr := `
repository:
  - remote: "https://example.com/repo.git"
    query_timeout: 60
storage:
  type: "memory"
service:
  worker_pool_size: 4
  repo_manager_clone_path: "/custom/clone"
  worker_root_path: "/custom/workers"
  max_message_bytes: 1000000
`
	cfg, err := ParseBytes([]byte(yamlStr))
	require.NoError(t, err)

	assert.Equal(t, StorageTypeMemory, cfg.Storage.Type)
	assert.Equal(t, int64(60), cfg.Repository[0].QueryTimeout)
	assert.Equal(t, "/custom/clone", cfg.Service.RepoManagerClonePath)
	assert.Equal(t, "/custom/workers", cfg.Service.WorkerRootPath)
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
  worker_pool_size: 1
`,
		},
		{
			name: "empty defaults to memory",
			yaml: `
repository:
  - remote: "https://example.com/r.git"
service:
  worker_pool_size: 1
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
  worker_pool_size: 1
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
  worker_pool_size: 1
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
  worker_pool_size: 1
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
  worker_pool_size: 1
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
  worker_pool_size: 1
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
  worker_pool_size: 0
`
	_, err := ParseBytes([]byte(yamlStr))
	require.Error(t, err)
}

func TestParseBytes_EmptyRemoteRejected(t *testing.T) {
	yamlStr := `
repository:
  - remote: ""
service:
  worker_pool_size: 1
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
  worker_pool_size: 1
`
	_, err := ParseBytes([]byte(yamlStr))
	require.Error(t, err)
}

func TestParseBytes_WorkerRootPathRequiresClonePath(t *testing.T) {
	yamlStr := `
repository:
  - remote: "https://example.com/repo.git"
service:
  worker_pool_size: 1
  worker_root_path: "/some/path"
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
