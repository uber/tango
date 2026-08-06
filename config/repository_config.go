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

// DefaultQueryTimeoutSeconds is the default query_timeout applied when the
// field is unset or zero (15 minutes), matching core/bazel's _queryTimeout.
const DefaultQueryTimeoutSeconds int64 = 900

// RepositoryConfig holds configuration for a single repository.
type RepositoryConfig struct {
	Remote                 string   `yaml:"remote"`
	FullHashRepos          []string `yaml:"full_hash_repos"`
	ExcludedFiles          []string `yaml:"excluded_files"`
	ExcludeExternalTargets bool     `yaml:"exclude_external_targets"`
	BzlmodEnabled          bool     `yaml:"bzlmod_enabled"`
	BazelCommand           string   `yaml:"bazel_command"`
	// QueryTimeout is the Bazel query timeout in seconds. Defaults to DefaultQueryTimeoutSeconds (900, i.e. 15 minutes).
	QueryTimeout   int64    `yaml:"query_timeout"`
	BazelExtraArgs []string `yaml:"bazel_extra_args"`
	// BazelStartupOptions are Bazel startup flags placed before the `query` subcommand (e.g. "--batch"); empty by default.
	BazelStartupOptions []string `yaml:"bazel_startup_options"`
	StreamBazelLogs     bool     `yaml:"stream_bazel_logs"`
	// DirectlyChangedAttributes restricts which rule attributes GetChangedTargets
	// treats as evidence that a target's own configuration changed, as opposed to
	// dependency-only churn.
	//
	// GetChangedTargets classifies each changed target either as "directly
	// changed" (distance 0 — its own definition differs between revisions) or as
	// "transitively changed" (distance > 0 — unchanged itself, but reachable from
	// a directly changed target). A target whose attribute set differs between
	// revisions is classified as directly changed.
	//
	// Bazel attaches many attributes to a rule that are pure BUILD-file
	// bookkeeping rather than semantic configuration — for example
	// generator_location records the generating macro's line:column, which
	// shifts whenever unrelated lines are added or removed earlier in the same
	// BUILD file, with no effect on the rule's behavior. Comparing the full,
	// unfiltered attribute set would classify that cosmetic churn as a direct
	// change and assign the target distance 0, when it should instead inherit
	// its distance from its dependencies.
	//
	// When DirectlyChangedAttributes is set, only attributes named here are
	// compared; all others are ignored for this classification. When unset
	// (the default), every attribute is compared and can trigger a direct-change
	// classification.
	DirectlyChangedAttributes []string `yaml:"directly_changed_attributes"`
}

// RepositoryConfigProvider looks up per-repository configuration by remote.
// Implementations may read from a local file, a remote config service, etc.
type RepositoryConfigProvider interface {
	GetRepositoryConfig(remote string) (RepositoryConfig, bool)
}
