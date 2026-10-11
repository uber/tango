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

// Supported GraphConfig.Format values.
const (
	// GraphFormatGob is the legacy gob-encoded chunk stream format.
	GraphFormatGob = "gob"
	// GraphFormatTGB is the columnar TGB blob format (internal/tgb).
	GraphFormatTGB = "tgb"
)

// GraphConfig holds graph storage settings for a single repository or the
// "default" fallback. Each entry in the top-level graph map is one of these.
type GraphConfig struct {
	// Format selects the storage format for cached target-graph blobs:
	// GraphFormatGob or GraphFormatTGB. The two formats live under different
	// cache keys, so flipping the value never reinterprets existing blobs —
	// a flip means cache misses that recompute, nothing more. With
	// GraphFormatTGB, reads still fall back to gob entries written before
	// the flip.
	Format string `yaml:"format"`
	// ShadowCompare, with GraphFormatTGB, additionally runs the incumbent
	// targetdiff comparison over the same two graphs in a background
	// goroutine on every GetChangedTargets request and emits a mismatch
	// metric (plus a detailed log) when the TGB path's result diverges.
	// It has no effect on what is served. Meaningless under GraphFormatGob.
	ShadowCompare bool `yaml:"shadow_compare"`
}

// GraphConfigProvider resolves graph storage settings by repository remote.
// Implementations look up the short remote name in a per-repo map, fall back
// to a "default" entry, or return an error when neither exists.
type GraphConfigProvider interface {
	GetGraphConfig(remote string) (GraphConfig, error)
}
