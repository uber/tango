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
