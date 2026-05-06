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
	// DefaultTargetChunkSize is the default number of targets per message chunk, used for streaming large target graphs.
	DefaultTargetChunkSize = 1000
)

// ToShortRemote returns the short remote name given a git ssh remote string.
// For example, "git@github:uber/tango" will return "uber/tango".
func ToShortRemote(remote string) string {
	strs := strings.Split(remote, ":")
	return strs[len(strs)-1]
}

// GetGraphByTreeHash returns the cache path for the target graph by treehash.
func GetGraphByTreeHash(remote, treehash string) string {
	return filepath.Join(ToShortRemote(remote), treehash)
}

// GetTreehashCachePath returns the cache path for the treehash.
// extraExcludeFilesRegex is folded into the key when non-empty so requests with
// different exclusion sets do not collide. Empty list ⇒ legacy path unchanged.
func GetTreehashCachePath(buildDescription *tangopb.BuildDescription, extraExcludeFilesRegex []string) string {
	return filepath.Join(ToShortRemote(buildDescription.Remote), fmt.Sprintf("treehash-map-%s", buildDescription.BaseSha), getReqsHash(buildDescription.Requests)) + "-" + buildDescription.Strategy.String() + getExtraExcludesSuffix(extraExcludeFilesRegex)
}

// GetComparedTargetsCachePath returns the cache path for a compared target graph result.
// treehash1 and treehash2 are the resolved treehashes of the first and second revisions.
// remote is the shared git remote for both revisions.
// extraExcludeFilesRegex is folded into the key when non-empty so requests with
// different exclusion sets do not collide. Empty list ⇒ legacy path unchanged.
func GetComparedTargetsCachePath(remote, treehash1, treehash2 string, extraExcludeFilesRegex []string) string {
	return filepath.Join("compared-targets", ToShortRemote(remote), treehash1, treehash2) + getExtraExcludesSuffix(extraExcludeFilesRegex)
}

// GetChangedTargetsAndEdgesCachePath returns the cache path for a GetChangedTargetsAndEdges result.
// treehash1 and treehash2 are the resolved treehashes of the first and second revisions.
// remote is the shared git remote for both revisions.
// extraExcludeFilesRegex is folded into the key when non-empty so requests with
// different exclusion sets do not collide. Empty list ⇒ legacy path unchanged.
func GetChangedTargetsAndEdgesCachePath(remote, treehash1, treehash2 string, extraExcludeFilesRegex []string) string {
	return filepath.Join("compared-targets-and-edges", ToShortRemote(remote), treehash1, treehash2) + getExtraExcludesSuffix(extraExcludeFilesRegex)
}

// getReqsHash returns a fixed-length MD5 hash of the sorted request URLs.
// Each URL's bytes are fed into the digest individually (no separator), matching
// the Java MessageDigest.update(str.getBytes()) per-string behavior.
func getReqsHash(requests []*tangopb.Request) string {
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

// getExtraExcludesSuffix returns "" for an empty list (preserves legacy cache
// paths), otherwise "-<md5 hex of sorted regexes>". Sort + per-string update
// mirrors getReqsHash's style.
func getExtraExcludesSuffix(regexes []string) string {
	if len(regexes) == 0 {
		return ""
	}
	sorted := make([]string, len(regexes))
	copy(sorted, regexes)
	sort.Strings(sorted)
	h := md5.New()
	for _, r := range sorted {
		h.Write([]byte(r))
	}
	return fmt.Sprintf("-%x", h.Sum(nil))
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
	responses = append(responses, &tangopb.GetTargetGraphResponse{
		Item: &tangopb.GetTargetGraphResponse_Metadata{
			Metadata: &tangopb.Metadata{
				TargetIdMapping:             targetIDToName,
				RuleTypeMapping:             ruleTypeIDToName,
				TagMapping:                  tagIDToName,
				AttributeNameMapping:        attrNameIDToName,
				AttributeStringValueMapping: attrStrValIDToVal,
			},
		},
	})

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
