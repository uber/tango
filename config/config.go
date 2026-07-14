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

const (
	_defaultBazelQueryTimeoutSeconds = 600
	_defaultMaxNumTargets            = 250
	_defaultMaxNumChangedTargets     = 125
	_defaultMaxNumMetadataEntries    = 50_000
)

var (
	_bzlmodEnabledDefault = true
)

var _ RepositoryConfigProvider = (*Config)(nil)

// Config is the root configuration structure.
type Config struct {
	Repository []RepositoryConfig `yaml:"repository"`
	Storage    StorageConfig      `yaml:"storage"`
	Service    ServiceConfig      `yaml:"service"`

	// repositoryByRemote is built at parse time for O(1) lookup.
	repositoryByRemote map[string]*RepositoryConfig
}

// GetRepositoryConfig returns the RepositoryConfig for the given remote URL.
// Returns a zero-value config and false if the remote is not found.
func (c *Config) GetRepositoryConfig(remote string) (RepositoryConfig, bool) {
	repo, ok := c.repositoryByRemote[remote]
	if !ok {
		return RepositoryConfig{}, false
	}
	return *repo, true
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
	if config.Service.WorkspacesRootPath == "" {
		return nil, fmt.Errorf("service.workspaces_root_path must be set")
	}
	if config.Service.WorkerRootPath == "" {
		config.Service.WorkerRootPath = filepath.Join(config.Service.WorkspacesRootPath, ".workers")
	}
	if config.Service.MaxWorkerPoolSize <= 0 {
		return nil, fmt.Errorf("service.max_worker_pool_size must be > 0, got %d", config.Service.MaxWorkerPoolSize)
	}
	if config.Service.Streaming.MaxNumTargets <= 0 {
		config.Service.Streaming.MaxNumTargets = _defaultMaxNumTargets
	}
	if config.Service.Streaming.MaxNumChangedTargets <= 0 {
		config.Service.Streaming.MaxNumChangedTargets = _defaultMaxNumChangedTargets
	}
	if config.Service.Streaming.MaxNumMetadataEntries <= 0 {
		config.Service.Streaming.MaxNumMetadataEntries = _defaultMaxNumMetadataEntries
	}
	config.repositoryByRemote = make(map[string]*RepositoryConfig, len(config.Repository))
	for i := range config.Repository {
		remote := config.Repository[i].Remote
		if remote == "" {
			return nil, fmt.Errorf("repository[%d].remote must not be empty", i)
		}
		if _, exists := config.repositoryByRemote[remote]; exists {
			return nil, fmt.Errorf("duplicate repository remote %q", remote)
		}
		if config.Repository[i].BzlmodEnabled == nil {
			config.Repository[i].BzlmodEnabled = &_bzlmodEnabledDefault
		}
		if config.Repository[i].QueryTimeoutSeconds <= 0 {
			config.Repository[i].QueryTimeoutSeconds = _defaultBazelQueryTimeoutSeconds
		}
		config.repositoryByRemote[remote] = &config.Repository[i]
	}
	return &config, nil
}
