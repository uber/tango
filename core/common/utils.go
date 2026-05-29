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
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/uber/tango/core/targethasher"
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

// GetGraphByTreeHash returns the cache path for the target graph by treehash.
// strategy is part of the key because different computation strategies (e.g.
// SHELL vs NATIVE) can produce different graphs from the same tree state.
// requestOptions is folded into the key when any of its fields affect computation
// (today: extra_exclude_files_regex). Empty/nil ⇒ legacy path unchanged.
func GetGraphByTreeHash(remote, treehash string, strategy tangopb.ComputationStrategy, requestOptions *tangopb.RequestOptions) string {
	path := filepath.Join(ToShortRemote(remote), "graphs", treehash, strategy.String())
	if hash := HashRequestOptions(requestOptions); hash != "" {
		path += "_requests-options-" + hash
	}
	return path
}

// GetTreehashCachePath returns the cache path for the treehash mapping.
// The git treehash is purely a function of git state (base SHA + applied
// requests), so neither requestOptions nor the computation strategy is part
// of this key.
func GetTreehashCachePath(buildDescription *tangopb.BuildDescription) string {
	path := filepath.Join(ToShortRemote(buildDescription.Remote), "treehashes", fmt.Sprintf("base-sha-%s", buildDescription.BaseSha))
	if len(buildDescription.Requests) > 0 {
		path += "_request-urls-" + GetReqURLsHash(buildDescription.Requests)
	}
	return path
}

// GetComparedTargetsCachePath returns the cache path for a compared target graph result.
// treehash1 and treehash2 are the resolved treehashes of the first and second revisions.
// remote is the shared git remote for both revisions.
// requestOptions is folded into the key when any of its fields affect computation.
// Empty/nil ⇒ legacy path unchanged.
func GetComparedTargetsCachePath(remote, treehash1, treehash2 string, requestOptions *tangopb.RequestOptions) string {
	path := filepath.Join(ToShortRemote(remote), "compared-targets", treehash1+"_"+treehash2)
	if hash := HashRequestOptions(requestOptions); hash != "" {
		path += "_requests-options-" + hash
	}
	return path
}

// GetChangedTargetsAndEdgesCachePath returns the cache path for a GetChangedTargetsAndEdges result.
// treehash1 and treehash2 are the resolved treehashes of the first and second revisions.
// remote is the shared git remote for both revisions.
// requestOptions is folded into the key when any of its fields affect computation.
// Empty/nil ⇒ legacy path unchanged.
func GetChangedTargetsAndEdgesCachePath(remote, treehash1, treehash2 string, requestOptions *tangopb.RequestOptions) string {
	path := filepath.Join(ToShortRemote(remote), "compared-targets-and-edges", treehash1+"_"+treehash2)
	if hash := HashRequestOptions(requestOptions); hash != "" {
		path += "_requests-options-" + hash
	}
	return path
}

// GetReqURLsHash returns a fixed-length MD5 hash of the sorted request URLs.
// Each URL's bytes are fed into the digest individually (no separator), matching
// the Java MessageDigest.update(str.getBytes()) per-string behavior.
func GetReqURLsHash(requests []*tangopb.Request) string {
	if len(requests) == 0 {
		return ""
	}
	urls := make([]string, 0, len(requests))
	for _, req := range requests {
		urls = append(urls, req.GetUrl())
	}
	sort.Strings(urls)
	h := md5.New()
	for _, url := range urls {
		h.Write([]byte(url))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// HashRequestOptions returns "" when no field of RequestOptions contributes to
// the cache (preserves legacy paths), otherwise the md5 hex digest of those
// fields. As new fields are added to RequestOptions, fold them into the digest
// here.
func HashRequestOptions(opts *tangopb.RequestOptions) string {
	if opts == nil {
		return ""
	}
	excludes := opts.GetExtraExcludeFilesRegex()
	if len(excludes) == 0 {
		return ""
	}
	sorted := make([]string, len(excludes))
	copy(sorted, excludes)
	sort.Strings(sorted)
	h := md5.New()
	for _, r := range sorted {
		h.Write([]byte(r))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ResultToGetTargetGraphResponse converts a Result to a GetTargetGraphResponse
func ResultToGetTargetGraphResponse(result targethasher.Result) ([]*tangopb.GetTargetGraphResponse, error) {
	// Map target names to ids. This list is topologically sorted, so the ids are stable.
	targetNamesMapping := make(map[string]int32, len(result.TargetNames))
	for i, name := range result.TargetNames {
		targetNamesMapping[name] = int32(i)
	}

	ruleTypeMapper := NewNameIDMapper()
	getRuleTypeID := func(key string) int32 { return ruleTypeMapper.ID(key) }

	tagMapper := NewNameIDMapper()
	getTagID := func(key string) int32 { return tagMapper.ID(key) }

	attrNameMapper := NewNameIDMapper()
	getAttrNameID := func(key string) int32 { return attrNameMapper.ID(key) }

	attrStrValMapper := NewNameIDMapper()
	getAttrStrValID := func(key string) int32 { return attrStrValMapper.ID(key) }

	// Build the optimized targets slice
	optimizedTargets := make([]*tangopb.OptimizedTarget, 0, len(result.Targets))

	for _, t := range result.Targets {
		nameID := targetNamesMapping[t.Name]

		depIDs := make([]int32, 0, len(t.Deps))
		for _, depName := range t.Deps {
			if _, ok := targetNamesMapping[depName]; !ok {
				continue
			}
			depIDs = append(depIDs, targetNamesMapping[depName])
		}

		ot := &tangopb.OptimizedTarget{
			Id:                 nameID,
			Hash:               hex.EncodeToString(t.Hash),
			DirectDependencies: depIDs,
		}

		// RuleType
		if t.RuleType != "" {
			id := getRuleTypeID(t.RuleType)
			ot.RuleType = id
		}

		// Tags
		if len(t.Tags) > 0 {
			tagIDs := make([]int32, 0, len(t.Tags))
			for _, tag := range t.Tags {
				tagIDs = append(tagIDs, getTagID(tag))
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
					nameID := getAttrNameID(*attr.Name)
					valID := getAttrStrValID(*attr.StringValue)
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
	responses := chunkTargets(optimizedTargets, DefaultTargetChunkSize)
	for _, meta := range ChunkMetadata(
		targetIDToName,
		ruleTypeIDToName,
		tagIDToName,
		attrNameIDToName,
		attrStrValIDToVal,
		DefaultMetadataMapChunkSize,
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
