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

package orchestrator

import (
	"context"
	"encoding/hex"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/uber/tango/core/common"
	"github.com/uber/tango/core/targethasher"
	"github.com/uber/tango/tangopb"
)

// _cancelCheckInterval is how often we poll ctx.Err() inside per-target hot loops.
// Picked to keep overhead negligible while still surfacing cancellation in <100ms
// for typical target rates.
const _cancelCheckInterval = 4096

// ResultToGetTargetGraphResponse converts a Result to a GetTargetGraphResponse
func ResultToGetTargetGraphResponse(ctx context.Context, result targethasher.Result) ([]*tangopb.GetTargetGraphResponse, error) {
	// Map target names to ids. This list is topologically sorted, so the ids are stable.
	// IDs start at 1 — 0 is reserved as the proto3 "unset" sentinel so consumers using
	// encoding/json (which honors `omitempty` on int32 fields) never silently lose a target.
	targetNamesMapping := make(map[string]int32, len(result.TargetNames))
	for i, name := range result.TargetNames {
		targetNamesMapping[name] = int32(i + 1)
	}

	ruleTypeMapper := common.NewNameIDMapper()
	tagMapper := common.NewNameIDMapper()
	attrNameMapper := common.NewNameIDMapper()
	attrStrValMapper := common.NewNameIDMapper()

	// Build the optimized targets slice
	optimizedTargets := make([]*tangopb.OptimizedTarget, 0, len(result.Targets))

	n := 0
	for _, t := range result.Targets {
		if n%_cancelCheckInterval == 0 {
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
	responses := chunkTargets(optimizedTargets, common.DefaultTargetChunkSize)
	for _, meta := range common.ChunkMetadata(
		targetIDToName,
		ruleTypeIDToName,
		tagIDToName,
		attrNameIDToName,
		attrStrValIDToVal,
		common.DefaultMetadataMapChunkSize,
	) {
		responses = append(responses, &tangopb.GetTargetGraphResponse{
			Item: &tangopb.GetTargetGraphResponse_Metadata{Metadata: meta},
		})
	}

	return responses, nil
}

func chunkTargets(targets []*tangopb.OptimizedTarget, chunkSize int) []*tangopb.GetTargetGraphResponse {
	if chunkSize <= 0 {
		chunkSize = common.DefaultTargetChunkSize
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
