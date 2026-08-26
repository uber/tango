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

// DefaultQueryTimeoutSeconds is the default query_timeout_seconds applied when
// the field is unset or zero (10 minutes).
const DefaultQueryTimeoutSeconds int64 = 600

// _bzlmodEnabledDefault is the default for BzlmodEnabled when unset. It's a
// var (not a const) so its address can be taken for the pointer default.
var _bzlmodEnabledDefault = true

// RepositoryConfig holds configuration for a single repository.
type RepositoryConfig struct {
	// Remote is the URL used to `git clone` the repository. Tango clones from
	// this URL and uses it as the lookup key for per-repo settings. Must be
	// unique across all entries and match exactly what clients send in
	// BuildDescription.remote.
	Remote string `yaml:"remote"`
	// RepositoryID is the required operator-provided name used for metrics,
	// repository workspaces, and cache keys. It must be safe for all three and
	// uniquely identify this repository across Tango installations.
	RepositoryID string `yaml:"repository_id"`
	// TODO: FullHashRepos, ExcludedFiles, and StreamBazelLogs are not
	// documented in config/README.md. Delete them if they turn out to be
	// unneeded, otherwise document them there.
	FullHashRepos []string `yaml:"full_hash_repos"`
	ExcludedFiles []string `yaml:"excluded_files"`
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
	// BazelStartupOptions are Bazel startup flags placed before the `query` subcommand (e.g. "--batch"); empty by default.
	BazelStartupOptions []string `yaml:"bazel_startup_options"`
	StreamBazelLogs     bool     `yaml:"stream_bazel_logs"`
	// QueryTimeoutSeconds is the Bazel query timeout in seconds. Defaults to DefaultQueryTimeoutSeconds (600).
	QueryTimeoutSeconds int64 `yaml:"query_timeout_seconds"`
	// SeedAttributes restricts which rule attributes GetChangedTargets
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
	// When SeedAttributes is set, only attributes named here are
	// compared; all others are ignored for this classification. When unset
	// (the default), every attribute is compared and can trigger a direct-change
	// classification.
	SeedAttributes []string `yaml:"seed_attributes"`
	// AllTargetsFiles lists repo-relative paths that, when their content
	// differs between the two revisions compared by GetChangedTargets,
	// cause every target in the second revision's graph to be reported as
	// changed. Intended for files that affect the build globally but are
	// invisible to the target graph (toolchain definitions, CI config,
	// Bazel wrapper scripts). Matching is by exact path. Empty disables.
	AllTargetsFiles []string `yaml:"all_targets_files"`
}

// RepositoryConfigProvider looks up per-repository configuration by remote.
// Implementations may read from a local file, a remote config service, etc.
type RepositoryConfigProvider interface {
	GetRepositoryConfig(remote string) (RepositoryConfig, bool)
}
