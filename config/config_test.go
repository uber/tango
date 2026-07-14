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

const _baseServiceYAML = `
service:
  workspaces_root_path: "/tmp/x"
  max_worker_pool_size: 1
`

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tango-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func TestParse_ServiceValidation(t *testing.T) {
	tests := []struct {
		name string
		give string
	}{
		{
			name: "workspaces_root_path required",
			give: `
service:
  max_worker_pool_size: 1
repository:
  - remote: "r1"
`,
		},
		{
			name: "max_worker_pool_size must be positive",
			give: `
service:
  workspaces_root_path: "/tmp/x"
  max_worker_pool_size: 0
repository:
  - remote: "r1"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(writeConfig(t, tt.give))
			require.Error(t, err)
		})
	}
}

func TestParse_ServiceDefaults(t *testing.T) {
	tests := []struct {
		name                string
		give                string
		wantMaxMessageBytes int
	}{
		{
			name: "max_message_bytes unset",
			give: _baseServiceYAML + `
repository:
  - remote: "r1"
`,
			wantMaxMessageBytes: 0,
		},
		{
			name: "max_message_bytes explicit value preserved",
			give: _baseServiceYAML + `
  max_message_bytes: 1000000
repository:
  - remote: "r1"
`,
			wantMaxMessageBytes: 1000000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse(writeConfig(t, tt.give))
			require.NoError(t, err)
			assert.Equal(t, tt.wantMaxMessageBytes, cfg.Service.MaxMessageBytes)
		})
	}
}

func TestParse_RepositoryDefaults(t *testing.T) {
	tests := []struct {
		name                    string
		give                    string
		wantBzlmodEnabled       bool
		wantQueryTimeoutSeconds int64
	}{
		{
			name: "bzlmod_enabled and query_timeout_seconds default when unset",
			give: _baseServiceYAML + `
repository:
  - remote: "r1"
`,
			wantBzlmodEnabled:       true,
			wantQueryTimeoutSeconds: _defaultBazelQueryTimeoutSeconds,
		},
		{
			name: "bzlmod_enabled explicit false preserved",
			give: _baseServiceYAML + `
repository:
  - remote: "r1"
    bzlmod_enabled: false
`,
			wantBzlmodEnabled:       false,
			wantQueryTimeoutSeconds: _defaultBazelQueryTimeoutSeconds,
		},
		{
			name: "query_timeout_seconds explicit value preserved",
			give: _baseServiceYAML + `
repository:
  - remote: "r1"
    query_timeout_seconds: 120
`,
			wantBzlmodEnabled:       true,
			wantQueryTimeoutSeconds: 120,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse(writeConfig(t, tt.give))
			require.NoError(t, err)
			repoCfg, ok := cfg.GetRepositoryConfig("r1")
			require.True(t, ok)
			require.NotNil(t, repoCfg.BzlmodEnabled)
			assert.Equal(t, tt.wantBzlmodEnabled, *repoCfg.BzlmodEnabled)
			assert.Equal(t, tt.wantQueryTimeoutSeconds, repoCfg.QueryTimeoutSeconds)
		})
	}
}
