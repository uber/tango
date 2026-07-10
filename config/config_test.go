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

	"github.com/stretchr/testify/require"
)

func TestParse_Validation(t *testing.T) {
	t.Parallel()

	validConfig := `
repository:
  - remote: "git@github.com:org/repo"
    query_timeout_seconds: 300
storage:
  type: memory
service:
  worker_pool_size: 5
  repo_manager_clone_path: /tmp/tango
  chunking:
    target_chunk_size: 250
    changed_target_chunk_size: 125
    metadata_map_chunk_size: 50000
`

	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{
			name: "valid config",
			yaml: validConfig,
		},
		{
			name: "worker_pool_size not set",
			yaml: `
repository:
  - remote: "git@github.com:org/repo"
    query_timeout_seconds: 300
service:
  chunking:
    target_chunk_size: 250
    changed_target_chunk_size: 125
    metadata_map_chunk_size: 50000
`,
			wantErr: true,
		},
		{
			name: "target_chunk_size not set",
			yaml: `
repository:
  - remote: "git@github.com:org/repo"
    query_timeout_seconds: 300
service:
  worker_pool_size: 5
  chunking:
    changed_target_chunk_size: 125
    metadata_map_chunk_size: 50000
`,
			wantErr: true,
		},
		{
			name: "changed_target_chunk_size not set",
			yaml: `
repository:
  - remote: "git@github.com:org/repo"
    query_timeout_seconds: 300
service:
  worker_pool_size: 5
  chunking:
    target_chunk_size: 250
    metadata_map_chunk_size: 50000
`,
			wantErr: true,
		},
		{
			name: "metadata_map_chunk_size not set",
			yaml: `
repository:
  - remote: "git@github.com:org/repo"
    query_timeout_seconds: 300
service:
  worker_pool_size: 5
  chunking:
    target_chunk_size: 250
    changed_target_chunk_size: 125
`,
			wantErr: true,
		},
		{
			name: "repository remote not set",
			yaml: `
repository:
  - query_timeout_seconds: 300
service:
  worker_pool_size: 5
  chunking:
    target_chunk_size: 250
    changed_target_chunk_size: 125
    metadata_map_chunk_size: 50000
`,
			wantErr: true,
		},
		{
			name: "query_timeout_seconds not set",
			yaml: `
repository:
  - remote: "git@github.com:org/repo"
service:
  worker_pool_size: 5
  chunking:
    target_chunk_size: 250
    changed_target_chunk_size: 125
    metadata_map_chunk_size: 50000
`,
			wantErr: true,
		},
		{
			name: "duplicate repository remote",
			yaml: `
repository:
  - remote: "git@github.com:org/repo"
    query_timeout_seconds: 300
  - remote: "git@github.com:org/repo"
    query_timeout_seconds: 300
service:
  worker_pool_size: 5
  chunking:
    target_chunk_size: 250
    changed_target_chunk_size: 125
    metadata_map_chunk_size: 50000
`,
			wantErr: true,
		},
		{
			name: "worker_root_path without repo_manager_clone_path",
			yaml: `
repository:
  - remote: "git@github.com:org/repo"
    query_timeout_seconds: 300
service:
  worker_pool_size: 5
  worker_root_path: /tmp/workers
  chunking:
    target_chunk_size: 250
    changed_target_chunk_size: 125
    metadata_map_chunk_size: 50000
`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte(tt.yaml), 0o644))

			cfg, err := Parse(path)
			if tt.wantErr {
				require.Error(t, err, "expected error when %s", tt.name)
			} else {
				require.NoError(t, err)
				require.NotNil(t, cfg)
			}
		})
	}
}
