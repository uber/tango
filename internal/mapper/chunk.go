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

package mapper

import (
	"fmt"

	"github.com/uber/tango/tangopb"
)

// sizer is satisfied by generated proto message types, which all expose a
// Size() method computing their exact serialized byte length with no
// allocation or marshaling.
type sizer interface {
	Size() int
}

// BySize splits items into consecutive runs whose cumulative Size() stays
// at or under maxMessageBytes. A single item larger than the budget ships
// alone since it can't be split further. Always returns at least one chunk:
// empty input yields a single empty chunk so callers always have a message
// to send on the stream. Returns an error if a multi-chunk split produces
// any empty chunk after the first (which would indicate a bug in the
// splitting logic).
func BySize[T sizer](items []T, maxMessageBytes int) ([][]T, error) {
	if len(items) == 0 {
		return [][]T{nil}, nil
	}
	chunks := make([][]T, 0, 1)
	var current []T
	currentBytes := 0
	for _, item := range items {
		itemBytes := item.Size()
		if len(current) > 0 && currentBytes+itemBytes > maxMessageBytes {
			chunks = append(chunks, current)
			current = nil
			currentBytes = 0
		}
		current = append(current, item)
		currentBytes += itemBytes
	}
	chunks = append(chunks, current)
	for i := 1; i < len(chunks); i++ {
		if len(chunks[i]) == 0 {
			return nil, fmt.Errorf("internal error: chunk %d of %d is empty", i, len(chunks))
		}
	}
	return chunks, nil
}

// ChunkMetadata splits the metadata maps into multiple Metadata messages so
// each stays at or under maxMessageBytes. The two large maps (target names,
// attribute string values) are split independently by measured entry wire
// size; consumers merge all Metadata messages before use. The small maps
// (rule_type, tag, attribute_name) are sent in the first chunk.
// Always returns at least one chunk so callers always have a message to
// send on the stream. Returns an error if a non-first chunk is completely
// empty (all maps nil/empty).
func ChunkMetadata(
	targetIDToName map[int32]string,
	ruleTypeIDToName map[int32]string,
	tagIDToName map[int32]string,
	attrNameIDToName map[int32]string,
	attrStrValIDToVal map[int32]string,
	maxMessageBytes int,
) ([]*tangopb.Metadata, error) {
	targetChunks := splitMapByBytes(targetIDToName, maxMessageBytes)
	attrValChunks := splitMapByBytes(attrStrValIDToVal, maxMessageBytes)

	chunks := make([]*tangopb.Metadata, 0, max(1, len(targetChunks)+len(attrValChunks)))
	for _, c := range targetChunks {
		chunks = append(chunks, &tangopb.Metadata{TargetIdMapping: c})
	}
	for _, c := range attrValChunks {
		chunks = append(chunks, &tangopb.Metadata{AttributeStringValueMapping: c})
	}
	if len(chunks) == 0 {
		chunks = append(chunks, &tangopb.Metadata{})
	}
	chunks[0].RuleTypeMapping = ruleTypeIDToName
	chunks[0].TagMapping = tagIDToName
	chunks[0].AttributeNameMapping = attrNameIDToName

	for i := 1; i < len(chunks); i++ {
		c := chunks[i]
		if len(c.TargetIdMapping) == 0 &&
			len(c.RuleTypeMapping) == 0 &&
			len(c.TagMapping) == 0 &&
			len(c.AttributeNameMapping) == 0 &&
			len(c.AttributeStringValueMapping) == 0 {
			return nil, fmt.Errorf("internal error: metadata chunk %d of %d is empty", i, len(chunks))
		}
	}

	return chunks, nil
}

func splitMapByBytes(m map[int32]string, maxBytes int) []map[int32]string {
	if len(m) == 0 {
		return nil
	}
	var chunks []map[int32]string
	current := make(map[int32]string)
	currentBytes := 0
	for k, v := range m {
		entryBytes := mapEntryWireSize(k, v)
		if len(current) > 0 && currentBytes+entryBytes > maxBytes {
			chunks = append(chunks, current)
			current = make(map[int32]string)
			currentBytes = 0
		}
		current[k] = v
		currentBytes += entryBytes
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

// mapEntryWireSize returns the serialized size of one map[int32]string entry,
// mirroring the generated Metadata.Size() computation exactly.
func mapEntryWireSize(k int32, v string) int {
	mapEntrySize := 1 + varintSize(uint64(k)) + 1 + len(v) + varintSize(uint64(len(v)))
	return mapEntrySize + 1 + varintSize(uint64(mapEntrySize))
}

func varintSize(x uint64) int {
	n := 1
	for x >= 0x80 {
		x >>= 7
		n++
	}
	return n
}
