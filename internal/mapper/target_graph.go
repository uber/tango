package mapper

import (
	"context"
	"encoding/hex"
	"errors"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/uber/tango/core/targethasher"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/mapper/idmapper"
	"github.com/uber/tango/tangopb"
)

const cancelCheckInterval = 4096

// ProtoToGetTargetGraphRequest converts a proto GetTargetGraphRequest to the
// domain type. Returns an error if req is nil or its BuildDescription fails
// validation (see ProtoToBuildDescription).
func ProtoToGetTargetGraphRequest(req *tangopb.GetTargetGraphRequest) (entity.GetTargetGraphRequest, error) {
	if req == nil {
		return entity.GetTargetGraphRequest{}, errors.New("get target graph request is required")
	}
	build, err := ProtoToBuildDescription(req.GetBuildDescription())
	if err != nil {
		return entity.GetTargetGraphRequest{}, err
	}
	return entity.GetTargetGraphRequest{
		Build:             build,
		ExcludeFilesRegex: req.GetRequestOptions().GetExtraExcludeFilesRegex(),
		BypassCache:       req.GetBypassCache(),
	}, nil
}

// ResultToGetTargetGraphResponse converts a targethasher.Result into chunked
// GetTargetGraphResponse messages ready for streaming or storage. Each message
// is bounded by maxMessageBytes of real wire size (see chunkTargets).
func ResultToGetTargetGraphResponse(ctx context.Context, result targethasher.Result, maxMessageBytes int) ([]*tangopb.GetTargetGraphResponse, error) {
	targetNamesMapping := make(map[string]int32, len(result.TargetNames))
	for i, name := range result.TargetNames {
		targetNamesMapping[name] = int32(i + 1)
	}

	ruleTypeMapper := idmapper.NewMapper()
	tagMapper := idmapper.NewMapper()
	attrNameMapper := idmapper.NewMapper()
	attrStrValMapper := idmapper.NewMapper()

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

		if t.RuleType != "" {
			ot.RuleType = ruleTypeMapper.ID(t.RuleType)
		}

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

	targetIDToName := make(map[int32]string, len(targetNamesMapping))
	for s, id := range targetNamesMapping {
		targetIDToName[id] = s
	}

	var responses []*tangopb.GetTargetGraphResponse
	targetChunks, err := BySize(optimizedTargets, maxMessageBytes)
	if err != nil {
		return nil, err
	}
	for _, chunk := range targetChunks {
		responses = append(responses, &tangopb.GetTargetGraphResponse{
			Item: &tangopb.GetTargetGraphResponse_Targets{
				Targets: &tangopb.OptimizedTargets{Targets: chunk},
			},
		})
	}
	metaChunks, err := ChunkMetadata(
		targetIDToName,
		ruleTypeMapper.Invert(),
		tagMapper.Invert(),
		attrNameMapper.Invert(),
		attrStrValMapper.Invert(),
		maxMessageBytes,
	)
	if err != nil {
		return nil, err
	}
	for _, meta := range metaChunks {
		responses = append(responses, &tangopb.GetTargetGraphResponse{
			Item: &tangopb.GetTargetGraphResponse_Metadata{Metadata: meta},
		})
	}

	return responses, nil
}
