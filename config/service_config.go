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

import "path/filepath"

// ServiceConfig holds operational configuration for the Tango service.
type ServiceConfig struct {
	// MaxWorkerPoolSize is the max number of concurrent requests per repository.
	// Each worker is a lightweight local clone (hardlinked to the origin, not a
	// full copy) that handles one request at a time. Must be greater than 0.
	MaxWorkerPoolSize int `yaml:"max_worker_pool_size"`
	// WorkspacesRoot is the root directory where Tango stores repository clones
	// and worker checkouts. Required. Layout: <workspaces_root>/<repo>/ for
	// origin clones and <workspaces_root>/.workers/<repo>/worker-{1..N}/ for
	// worker checkouts.
	WorkspacesRoot string      `yaml:"workspaces_root"`
	Streaming      ChunkConfig `yaml:"streaming"` // streaming chunk sizes; zero values fall back to package defaults
}

// WorkerRootPath returns the root directory for worker workspace checkouts,
// as documented on WorkspacesRoot: <workspaces_root>/.workers.
func (s ServiceConfig) WorkerRootPath() string {
	return filepath.Join(s.WorkspacesRoot, ".workers")
}

// ChunkConfig controls the number of entries per gRPC stream message.
// All fields are optional; a zero value means "use the package default".
// Tune these when a monorepo's per-target size causes messages to approach
// gRPC's default 4MB per-message limit.
type ChunkConfig struct {
	// MaxNumTargets is the max number of OptimizedTarget entries per stream message.
	MaxNumTargets int `yaml:"max_num_targets"`
	// MaxNumChangedTargets is the max number of ChangedTarget entries per stream message.
	// ChangedTarget carries both old and new targets (~2× the size of a regular target).
	MaxNumChangedTargets int `yaml:"max_num_changed_targets"`
	// MaxNumMetadataEntries is the max number of entries per metadata map chunk.
	// Applies to target_id_mapping and attribute_string_value_mapping.
	MaxNumMetadataEntries int `yaml:"max_num_metadata_entries"`
}
