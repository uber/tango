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

// RepositoryConfig holds configuration for a single repository.
type RepositoryConfig struct {
	// Remote is the URL used to `git clone` the repository. Tango clones from
	// this URL and uses it as the lookup key for per-repo settings. Must be
	// unique across all entries and match exactly what clients send in
	// BuildDescription.remote.
	Remote string `yaml:"remote"`
	// BzlmodEnabled indicates whether this repository uses Bzlmod for external
	// dependency management. Defaults to true if unset. Set to false only for
	// repositories still using WORKSPACE.
	BzlmodEnabled *bool `yaml:"bzlmod_enabled"`
	// BazelCommandPath overrides the Bazel binary path. When empty, Tango
	// automatically downloads and caches Bazelisk from GitHub.
	BazelCommandPath string `yaml:"bazel_command_path"`
	// BazelExtraArgs are extra arguments passed to `bazel query` invocations,
	// inserted between the `query` subcommand and the query expression.
	BazelExtraArgs []string `yaml:"bazel_extra_args"`
	// QueryTimeoutSeconds is the Bazel query timeout in seconds. Defaults to 600.
	QueryTimeoutSeconds int64 `yaml:"query_timeout_seconds"`
}

// RepositoryConfigProvider looks up per-repository configuration by remote.
// Implementations may read from a local file, a remote config service, etc.
type RepositoryConfigProvider interface {
	GetRepositoryConfig(remote string) (RepositoryConfig, bool)
}
