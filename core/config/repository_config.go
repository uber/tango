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
