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

const (
	// DefaultMaxMessageBytes is the default value for ServiceConfig.MaxMessageBytes.
	// Matches today's metadata chunk size exactly (50,000 entries × ~85 bytes).
	DefaultMaxMessageBytes = 4_250_000

	// bytesPerTargetEstimate is a worst-case size assumption for a single OptimizedTarget
	// (target with ~10K direct deps × 4 bytes), used to convert a byte budget into a chunk size.
	bytesPerTargetEstimate = 40_000

	// bytesPerMetadataEntryEstimate is a worst-case size assumption for a single metadata
	// map entry (~60-char avg target name + proto overhead), used the same way.
	bytesPerMetadataEntryEstimate = 85
)

// ChunkSizesForByteBudget converts a MaxMessageBytes budget into the max number of
// targets, changed targets, and metadata entries per gRPC stream message, so responses
// stay under the 64MB default gRPC per-message limit. The conversion happens once — at
// controller/orchestrator wiring time, not per-request — using fixed size assumptions
// rather than measuring real messages. A ChangedTarget carries both an old and new
// target, so it gets half the plain-target budget. maxMessageBytes <= 0 falls back to
// DefaultMaxMessageBytes.
func ChunkSizesForByteBudget(maxMessageBytes int) (targetChunkSize, changedTargetChunkSize, metadataMapChunkSize int) {
	if maxMessageBytes <= 0 {
		maxMessageBytes = DefaultMaxMessageBytes
	}
	return max(1, maxMessageBytes/bytesPerTargetEstimate),
		max(1, maxMessageBytes/(2*bytesPerTargetEstimate)),
		max(1, maxMessageBytes/bytesPerMetadataEntryEstimate)
}
