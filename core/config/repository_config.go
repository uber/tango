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

// RepositoryConfig holds configuration for the repository.
type RepositoryConfig struct {
	Remote                 string   `yaml:"remote"`
	DefaultBranch          string   `yaml:"default_branch"`
	FullHashRepos          []string `yaml:"full_hash_repos"`
	ExcludedFiles          []string `yaml:"excluded_files"`
	ExcludeExternalTargets bool     `yaml:"exclude_external_targets"`
	BzlmodEnabled          bool     `yaml:"bzlmod_enabled"`
	BazelCommand           string   `yaml:"bazel_command"`
	QueryTimeout           int64    `yaml:"query_timeout"`           // in seconds
	GitTimeout             int64    `yaml:"git_timeout"`             // in seconds
	WorkspacePoolSize      int      `yaml:"workspace_pool_size"`     // number of worker workspaces per repo
	RepoManagerClonePath   string   `yaml:"repo_manager_clone_path"` // root directory for origin repo clones
	WorkerRootPath         string   `yaml:"worker_root_path"`        // root directory for worker workspace checkouts; defaults to repo_manager_clone_path/.workers
}
