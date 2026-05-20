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

// ServiceConfig holds operational configuration for the Tango service.
type ServiceConfig struct {
	WorkerPoolSize       int         `yaml:"worker_pool_size"`        // number of worker workspaces per repo
	RepoManagerClonePath string      `yaml:"repo_manager_clone_path"` // root directory for origin repo clones
	WorkerRootPath       string      `yaml:"worker_root_path"`        // root directory for worker workspace checkouts; defaults to repo_manager_clone_path/.workers
	Chunking             ChunkConfig `yaml:"chunking"`                // streaming chunk sizes; zero values fall back to package defaults
}

// ChunkConfig controls the number of entries per gRPC stream message.
// All fields are optional; a zero value means "use the package default".
// Tune these when a monorepo's per-target size causes messages to approach
// the 64MB default gRPC per-message limit.
type ChunkConfig struct {
	// TargetChunkSize is the max number of OptimizedTarget entries per stream message.
	TargetChunkSize int `yaml:"target_chunk_size"`
	// ChangedTargetChunkSize is the max number of ChangedTarget entries per stream message.
	// ChangedTarget carries both old and new targets (~2× the size of a regular target).
	ChangedTargetChunkSize int `yaml:"changed_target_chunk_size"`
	// MetadataMapChunkSize is the max number of entries per metadata map chunk.
	// Applies to target_id_mapping and attribute_string_value_mapping.
	MetadataMapChunkSize int `yaml:"metadata_map_chunk_size"`
}
