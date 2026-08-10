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
	// MaxWorkerPoolSize is the max number of concurrent requests per repository.
	// Each worker is a lightweight local clone (hardlinked to the origin, not a
	// full copy) that handles one request at a time. Must be greater than 0.
	MaxWorkerPoolSize int `yaml:"max_worker_pool_size"`
	// WorkspacesRootPath is the root directory where Tango stores repository
	// clones and worker checkouts. Required. Layout: <workspaces_root_path>/<repo>/
	// for origin clones and <workspaces_root_path>/.workers/<repo>/worker-{1..N}/
	// for worker checkouts.
	WorkspacesRootPath string `yaml:"workspaces_root_path"`
	MaxMessageBytes    int    `yaml:"max_message_bytes"` // max serialized bytes per streamed gRPC message; 0 → DefaultMaxMessageBytes
	// GraphFormat selects the storage format for cached target-graph blobs:
	// GraphFormatGob (default) or GraphFormatTGB. The two formats live under
	// different cache keys, so flipping the value never reinterprets existing
	// blobs — a flip means cache misses that recompute, nothing more. With
	// GraphFormatTGB, reads still fall back to gob entries written before the
	// flip.
	GraphFormat string `yaml:"graph_format"`
	// ShadowCompare, with GraphFormatTGB, additionally runs the incumbent
	// targetdiff comparison over the same two graphs in a background goroutine
	// on every GetChangedTargets request and emits a mismatch metric (plus a
	// detailed log) when the TGB path's result diverges. It has no effect on
	// what is served. Meaningless under GraphFormatGob.
	ShadowCompare bool `yaml:"shadow_compare"`
}

// Supported ServiceConfig.GraphFormat values.
const (
	// GraphFormatGob is the legacy gob-encoded chunk stream format.
	GraphFormatGob = "gob"
	// GraphFormatTGB is the columnar TGB blob format (internal/tgb).
	GraphFormatTGB = "tgb"
)

// DefaultMaxMessageBytes is the fallback max serialized size per streamed
// message (~4.25 MB), well under the 64 MB default gRPC limit.
const DefaultMaxMessageBytes = 4_250_000
