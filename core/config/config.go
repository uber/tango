package config

import (
	"os"

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
	return &config, nil
}
