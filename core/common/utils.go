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

package common

import (
	"context"
	"encoding/hex"
	"strings"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/uber/tango/core/targethasher"
	"github.com/uber/tango/internal/mapper/idmapper"
	"github.com/uber/tango/tangopb"
)

const (
	// DefaultTargetChunkSize is the default number of OptimizedTarget entries per stream message.
	// Sized conservatively: at ~40KB/target worst-case (target with ~10K direct deps × 4 bytes),
	// 250 targets ≈ 10MB — well under the 64MB default gRPC per-message limit.
	DefaultTargetChunkSize = 250

	// DefaultChangedTargetChunkSize is the default number of ChangedTarget entries per stream message.
	// A ChangedTarget carries both old_target and new_target (2× an OptimizedTarget), so we use
	// half the regular chunk size to stay within the same byte budget.
	DefaultChangedTargetChunkSize = 125

	// DefaultMetadataMapChunkSize is the max entries per metadata message chunk.
	// target_id_mapping and attribute_string_value_mapping scale with repo size and can exceed
	// the 64MB gRPC message limit for large monorepos, so they are split across multiple messages.
	// At ~85 bytes/entry (60-char avg target name + proto overhead), 50 000 entries ≈ 4.25MB per chunk.
	DefaultMetadataMapChunkSize = 50_000
)

// ToShortRemote returns the short remote name given a git ssh remote string.
// For example, "git@github:uber/tango" will return "uber/tango".
func ToShortRemote(remote string) string {
	strs := strings.Split(remote, ":")
	return strs[len(strs)-1]
}

// cancelCheckInterval is how often we poll ctx.Err() inside per-target hot loops.
// Picked to keep overhead negligible while still surfacing cancellation in <100ms
// for typical target rates.
const cancelCheckInterval = 4096

// ResultToGetTargetGraphResponse converts a Result to a GetTargetGraphResponse.
// targetChunkSize controls how many OptimizedTarget entries per stream message.
// metadataMapChunkSize controls how many entries per metadata map chunk.
// TODO: move this function to internal/mapper
func ResultToGetTargetGraphResponse(ctx context.Context, result targethasher.Result, targetChunkSize, metadataMapChunkSize int) ([]*tangopb.GetTargetGraphResponse, error) {
	// Map target names to ids. This list is topologically sorted, so the ids are stable.
	// IDs start at 1 — 0 is reserved as the proto3 "unset" sentinel so consumers using
	// encoding/json (which honors `omitempty` on int32 fields) never silently lose a target.
	targetNamesMapping := make(map[string]int32, len(result.TargetNames))
	for i, name := range result.TargetNames {
		targetNamesMapping[name] = int32(i + 1)
	}

	ruleTypeMapper := idmapper.NewMapper()
	tagMapper := idmapper.NewMapper()
	attrNameMapper := idmapper.NewMapper()
	attrStrValMapper := idmapper.NewMapper()

	// Build the optimized targets slice
	optimizedTargets := make([]*tangopb.OptimizedTarget, 0, len(result.Targets))

	n := 0
	for _, t := range result.Targets {
		if n%cancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		n++
		nameID := targetNamesMapping[t.Name]

		depIDs := make([]int32, 0, len(t.Deps))
		for _, depName := range t.Deps {
			depID, ok := targetNamesMapping[depName]
			if !ok {
				continue
			}
			depIDs = append(depIDs, depID)
		}

		ot := &tangopb.OptimizedTarget{
			Id:                 nameID,
			Hash:               hex.EncodeToString(t.Hash),
			DirectDependencies: depIDs,
		}

		// RuleType
		if t.RuleType != "" {
			id := ruleTypeMapper.ID(t.RuleType)
			ot.RuleType = id
		}

		// Tags
		if len(t.Tags) > 0 {
			tagIDs := make([]int32, 0, len(t.Tags))
			for _, tag := range t.Tags {
				tagIDs = append(tagIDs, tagMapper.ID(tag))
			}
			ot.Tags = tagIDs
		}
		ot.Root = t.Root
		ot.External = t.External
		if len(t.Attributes) > 0 {
			attrs := make(map[int32]int32, len(t.Attributes))
			for _, attr := range t.Attributes {
				// Only include STRING attributes with non-nil name and value to avoid nil dereferences.
				if attr.GetType() == buildpb.Attribute_STRING && attr.Name != nil && attr.StringValue != nil {
					nameID := attrNameMapper.ID(*attr.Name)
					valID := attrStrValMapper.ID(*attr.StringValue)
					attrs[nameID] = valID
				}
			}
			ot.Attributes = attrs
		}

		optimizedTargets = append(optimizedTargets, ot)
	}

	// Invert mappings: string -> id  =>  id -> string
	targetIDToName := make(map[int32]string, len(targetNamesMapping))
	for s, id := range targetNamesMapping {
		targetIDToName[id] = s
	}

	ruleTypeIDToName := ruleTypeMapper.Invert()
	tagIDToName := tagMapper.Invert()
	attrNameIDToName := attrNameMapper.Invert()
	attrStrValIDToVal := attrStrValMapper.Invert()

	// chunk targets into multiple messages for streaming
	responses := chunkTargets(optimizedTargets, targetChunkSize)
	for _, meta := range ChunkMetadata(
		targetIDToName,
		ruleTypeIDToName,
		tagIDToName,
		attrNameIDToName,
		attrStrValIDToVal,
		metadataMapChunkSize,
	) {
		responses = append(responses, &tangopb.GetTargetGraphResponse{
			Item: &tangopb.GetTargetGraphResponse_Metadata{Metadata: meta},
		})
	}

	return responses, nil
}

func chunkTargets(targets []*tangopb.OptimizedTarget, chunkSize int) []*tangopb.GetTargetGraphResponse {
	if chunkSize <= 0 {
		chunkSize = DefaultTargetChunkSize
	}

	// at least one chunk
	numChunks := max(1, (len(targets)+chunkSize-1)/chunkSize)

	responses := make([]*tangopb.GetTargetGraphResponse, 0, numChunks)

	for i := 0; i < len(targets); i += chunkSize {
		end := i + chunkSize
		if end > len(targets) {
			end = len(targets)
		}

		chunk := targets[i:end]
		responses = append(responses, &tangopb.GetTargetGraphResponse{
			Item: &tangopb.GetTargetGraphResponse_Targets{
				Targets: &tangopb.OptimizedTargets{
					Targets: chunk,
				},
			},
		})
	}

	// Handle empty targets case
	if len(responses) == 0 {
		responses = append(responses, &tangopb.GetTargetGraphResponse{
			Item: &tangopb.GetTargetGraphResponse_Targets{
				Targets: &tangopb.OptimizedTargets{
					Targets: []*tangopb.OptimizedTarget{},
				},
			},
		})
	}

	return responses
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
