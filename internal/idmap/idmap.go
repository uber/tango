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

// Package idmap provides the ID-mapping and metadata-chunking primitives used
// to build the tangopb wire format's int32-ID-keyed maps
package idmap

import "github.com/uber/tango/tangopb"

const (
	// DefaultTargetChunkSize is the default number of OptimizedTarget entries per stream message.
	// Sized conservatively: at ~40KB/target worst-case (target with ~10K direct deps × 4 bytes),
	// 250 targets ≈ 10MB — well under the 64MB default gRPC per-message limit.
	DefaultTargetChunkSize = 250

	// DefaultMetadataMapChunkSize is the max entries per metadata message chunk.
	// target_id_mapping and attribute_string_value_mapping scale with repo size and can exceed
	// the 64MB gRPC message limit for large monorepos, so they are split across multiple messages.
	// At ~85 bytes/entry (60-char avg target name + proto overhead), 50 000 entries ≈ 4.25MB per chunk.
	DefaultMetadataMapChunkSize = 50_000
)

// NameIDMapper assigns stable int32 IDs to string names on demand.
// IDs are assigned sequentially starting from 1. Zero is reserved as the
// proto3 "unset" sentinel so consumers using encoding/json (which honors
// `omitempty` on int32 fields) or any client that treats GetId() == 0 as
// missing never silently lose real entries.
type NameIDMapper struct {
	nameToID map[string]int32
	nextID   int32
}

// NewNameIDMapper creates a new NameIdMapper.
func NewNameIDMapper() *NameIDMapper {
	return &NameIDMapper{
		nameToID: make(map[string]int32),
		nextID:   1,
	}
}

// ID returns the existing ID for the provided name or assigns a new one.
func (a *NameIDMapper) ID(name string) int32 {
	if id, ok := a.nameToID[name]; ok {
		return id
	}
	id := a.nextID
	a.nextID++
	a.nameToID[name] = id
	return id
}

// Invert returns an id->name map built from the current name->id map.
func (a *NameIDMapper) Invert() map[int32]string {
	out := make(map[int32]string, len(a.nameToID))
	for name, id := range a.nameToID {
		out[id] = name
	}
	return out
}

// ChunkMetadata splits the metadata maps into multiple Metadata messages.
// target_id_mapping and attribute_string_value_mapping scale with repo size and can exceed the
// 64MB gRPC per-message limit for large monorepos; they are split across chunks of chunkSize entries.
// The small maps (rule_type, tag, attribute_name) always fit in one message and are sent in the first chunk.
func ChunkMetadata(
	targetIDToName map[int32]string,
	ruleTypeIDToName map[int32]string,
	tagIDToName map[int32]string,
	attrNameIDToName map[int32]string,
	attrStrValIDToVal map[int32]string,
	chunkSize int,
) []*tangopb.Metadata {
	if chunkSize <= 0 {
		chunkSize = DefaultMetadataMapChunkSize
	}

	targetChunks := splitMap(targetIDToName, chunkSize)
	attrValChunks := splitMap(attrStrValIDToVal, chunkSize)

	numChunks := max(1, max(len(targetChunks), len(attrValChunks)))
	chunks := make([]*tangopb.Metadata, 0, numChunks)

	for i := range numChunks {
		meta := &tangopb.Metadata{}
		// Small maps are always small enough to fit in one message; include them in the first chunk.
		if i == 0 {
			meta.RuleTypeMapping = ruleTypeIDToName
			meta.TagMapping = tagIDToName
			meta.AttributeNameMapping = attrNameIDToName
		}
		if i < len(targetChunks) {
			meta.TargetIdMapping = targetChunks[i]
		}
		if i < len(attrValChunks) {
			meta.AttributeStringValueMapping = attrValChunks[i]
		}
		chunks = append(chunks, meta)
	}

	return chunks
}

// splitMap splits a map[int32]string into slices of at most size entries each.
func splitMap(m map[int32]string, size int) []map[int32]string {
	if len(m) == 0 {
		return nil
	}
	chunks := make([]map[int32]string, 0, (len(m)+size-1)/size)
	current := make(map[int32]string, size)
	for k, v := range m {
		current[k] = v
		if len(current) >= size {
			chunks = append(chunks, current)
			current = make(map[int32]string, size)
		}
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}
