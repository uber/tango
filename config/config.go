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
	"bytes"
	"fmt"
	"os"

	yaml "github.com/goccy/go-yaml"
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
	return ParseBytes(yamlBytes)
}

// ParseBytes parses the full configuration from raw YAML bytes.
// Unknown YAML fields are rejected.
func ParseBytes(yamlBytes []byte) (*Config, error) {
	var config Config
	dec := yaml.NewDecoder(bytes.NewReader(yamlBytes), yaml.DisallowUnknownField())
	if err := dec.Decode(&config); err != nil {
		return nil, err
	}

	// --- storage validation ---
	switch config.Storage.Type {
	case StorageTypeMemory, "":
		config.Storage.Type = StorageTypeMemory
	case StorageTypeDisk:
		if config.Storage.Disk == nil || config.Storage.Disk.RootPath == "" {
			return nil, fmt.Errorf("storage.disk.root_path must be set when storage type is %q", StorageTypeDisk)
		}
	default:
		return nil, fmt.Errorf("unsupported storage type: %q (supported: %q, %q)", config.Storage.Type, StorageTypeMemory, StorageTypeDisk)
	}

	// --- service validation and defaults ---
	if config.Service.WorkspacesRootPath == "" {
		return nil, fmt.Errorf("service.workspaces_root_path must be set")
	}
	if config.Service.MaxWorkerPoolSize <= 0 {
		return nil, fmt.Errorf("service.max_worker_pool_size must be > 0, got %d", config.Service.MaxWorkerPoolSize)
	}
	if config.Service.MaxMessageBytes <= 0 {
		config.Service.MaxMessageBytes = DefaultMaxMessageBytes
	}
	switch config.Service.GraphFormat {
	case "":
		config.Service.GraphFormat = GraphFormatGob
	case GraphFormatGob, GraphFormatTGB:
	default:
		return nil, fmt.Errorf("unsupported service.graph_format: %q (supported: %q, %q)", config.Service.GraphFormat, GraphFormatGob, GraphFormatTGB)
	}

	// --- repository validation and defaults ---
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
		// Default query_timeout_seconds: 600 seconds (10 minutes).
		if config.Repository[i].QueryTimeoutSeconds <= 0 {
			config.Repository[i].QueryTimeoutSeconds = DefaultQueryTimeoutSeconds
		}
		config.repositoryByRemote[remote] = &config.Repository[i]
	}
	return &config, nil
}
