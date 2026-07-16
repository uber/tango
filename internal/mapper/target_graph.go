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

// ResultToTargetGraph converts a targethasher.Result into a proto-free
// entity.TargetGraph. Targets are emitted in the topological order given
// by result.TargetNames. Only STRING attributes with non-nil name and
// value are included.
func ResultToTargetGraph(ctx context.Context, result targethasher.Result) (entity.TargetGraph, error) {
	targets := make([]entity.OptimizedTarget, 0, len(result.TargetNames))
	for i, name := range result.TargetNames {
		if i%cancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return entity.TargetGraph{}, err
			}
		}
		t, ok := result.Targets[name]
		if !ok {
			continue
		}
		ot := entity.OptimizedTarget{
			Name:         name,
			Hash:         hex.EncodeToString(t.Hash),
			Dependencies: t.Deps,
			RuleType:     t.RuleType,
			Tags:         t.Tags,
			Root:         t.Root,
			External:     t.External,
		}
		if len(t.Attributes) > 0 {
			attrs := make(map[string]string, len(t.Attributes))
			for _, attr := range t.Attributes {
				if attr.GetType() == buildpb.Attribute_STRING && attr.Name != nil && attr.StringValue != nil {
					attrs[*attr.Name] = *attr.StringValue
				}
			}
			if len(attrs) > 0 {
				ot.Attributes = attrs
			}
		}
		targets = append(targets, ot)
	}
	return entity.TargetGraph{Targets: targets}, nil
}

// TargetGraphToProto converts an entity.TargetGraph into chunked
// GetTargetGraphResponse messages ready for streaming. Each message is
// bounded by maxMessageBytes of real wire size.
func TargetGraphToProto(ctx context.Context, graph entity.TargetGraph, maxMessageBytes int) ([]*tangopb.GetTargetGraphResponse, error) {
	targetNamesMapping := make(map[string]int32, len(graph.Targets))
	for i, t := range graph.Targets {
		targetNamesMapping[t.Name] = int32(i + 1)
	}

	ruleTypeMapper := idmapper.NewMapper()
	tagMapper := idmapper.NewMapper()
	attrNameMapper := idmapper.NewMapper()
	attrStrValMapper := idmapper.NewMapper()

	optimizedTargets := make([]*tangopb.OptimizedTarget, 0, len(graph.Targets))
	for i, t := range graph.Targets {
		if i%cancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		depIDs := make([]int32, 0, len(t.Dependencies))
		for _, depName := range t.Dependencies {
			depID, ok := targetNamesMapping[depName]
			if !ok {
				continue
			}
			depIDs = append(depIDs, depID)
		}

		ot := &tangopb.OptimizedTarget{
			Id:                 targetNamesMapping[t.Name],
			Hash:               t.Hash,
			DirectDependencies: depIDs,
			Root:               t.Root,
			External:           t.External,
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
		if len(t.Attributes) > 0 {
			attrs := make(map[int32]int32, len(t.Attributes))
			for k, v := range t.Attributes {
				attrs[attrNameMapper.ID(k)] = attrStrValMapper.ID(v)
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

// ResultToGetTargetGraphResponse is a convenience that composes
// ResultToTargetGraph and TargetGraphToProto.
func ResultToGetTargetGraphResponse(ctx context.Context, result targethasher.Result, maxMessageBytes int) ([]*tangopb.GetTargetGraphResponse, error) {
	graph, err := ResultToTargetGraph(ctx, result)
	if err != nil {
		return nil, err
	}
	return TargetGraphToProto(ctx, graph, maxMessageBytes)
}
