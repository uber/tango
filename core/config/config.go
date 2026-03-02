package config

import (
	"fmt"
	"os"
	"path/filepath"

	yaml "github.com/goccy/go-yaml"
)

// Config is the root configuration structure.
type Config struct {
	Repository RepositoryConfig `yaml:"repository"`
	Storage    StorageConfig    `yaml:"storage"`
}

// Parse parses the full configuration from the given file path.
func Parse(configFilePath string) (*Config, error) {
	yamlBytes, err := os.ReadFile(configFilePath)
	if err != nil {
		return nil, err
	}
	var config Config
	if err := yaml.Unmarshal(yamlBytes, &config); err != nil {
		return nil, err
	}
	// Default to memory storage if not specified
	if config.Storage.Type == "" {
		config.Storage.Type = StorageTypeMemory
	}
	if config.Repository.WorkerRootPath != "" && config.Repository.RepoManagerClonePath == "" {
		return nil, fmt.Errorf("repository.repo_manager_clone_path must be set when worker_root_path is specified")
	}
	if config.Repository.RepoManagerClonePath == "" {
		config.Repository.RepoManagerClonePath = filepath.Join(os.TempDir(), "tango-repo-manager")
	}
	if config.Repository.WorkerRootPath == "" {
		config.Repository.WorkerRootPath = filepath.Join(config.Repository.RepoManagerClonePath, ".workers")
	}
	if config.Repository.WorkspacePoolSize <= 0 {
		return nil, fmt.Errorf("repository.workspace_pool_size must be > 0, got %d", config.Repository.WorkspacePoolSize)
	}
	return &config, nil
}
