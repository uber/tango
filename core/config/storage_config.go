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

// StorageType represents the type of storage backend.
type StorageType string

const (
	StorageTypeMemory StorageType = "memory"
	StorageTypeDisk   StorageType = "disk"
)

// StorageConfig holds configuration for storage backends.
// Similar to Athens' storage configuration pattern.
type StorageConfig struct {
	// Type specifies which storage backend to use.
	// Supported values: "memory", "disk" etc.
	// Defaults to "memory" if not specified.
	Type StorageType `yaml:"type"`

	// Disk holds configuration for disk-based storage.
	Disk *DiskStorageConfig `yaml:"disk,omitempty"`
}

// DiskStorageConfig holds configuration for disk-based storage.
type DiskStorageConfig struct {
	// RootPath is the directory where blobs will be stored.
	RootPath string `yaml:"root_path"`
}
