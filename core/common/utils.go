package common

import (
	"encoding/base64"
	"path/filepath"
	"strings"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/uber/tango/core/targethasher"
	"github.com/uber/tango/tangopb"
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
func GetTreehashCachePath(buildDescription *tangopb.BuildDescription) string {
	return filepath.Join(ToShortRemote(buildDescription.Remote), buildDescription.BaseSha, getReqsBase64(buildDescription.RequestUrls))
}

// getReqsBase64 returns the base64 encoded request URLs.
func getReqsBase64(requestURLs []string) string {
	encodedURLs := make([]string, 0, len(requestURLs))
	for _, url := range requestURLs {
		encoded := base64.RawURLEncoding.EncodeToString([]byte(url))
		encodedURLs = append(encodedURLs, encoded)
	}
	return strings.Join(encodedURLs, "-")
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
			Hash:               string(t.Hash),
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

	// Assemble final OptimizedTargets
	return []*tangopb.GetTargetGraphResponse{
		{
			Item: &tangopb.GetTargetGraphResponse_Targets{
				Targets: &tangopb.OptimizedTargets{
					Targets: optimizedTargets,
				},
			},
		},
		{
			Item: &tangopb.GetTargetGraphResponse_Metadata{
				Metadata: &tangopb.Metadata{
					TargetIdMapping:             targetIDToName,
					RuleTypeMapping:             ruleTypeIDToName,
					TagMapping:                  tagIDToName,
					AttributeNameMapping:        attrNameIDToName,
					AttributeStringValueMapping: attrStrValIDToVal,
				},
			},
		},
	}, nil
}
