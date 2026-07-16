package mapper

import (
	"context"
	"encoding/hex"
	"errors"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/uber/tango/core/targethasher"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/mapper/idmapper"
	"github.com/uber/tango/internal/streaming"
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

// TargetGraphToChunks converts a string-based entity.TargetGraph into
// ID-mapped entity.GraphChunk slices, ready for storage or proto
// conversion. Each chunk is bounded by maxMessageBytes of estimated wire
// size.
func TargetGraphToChunks(ctx context.Context, graph entity.TargetGraph, maxMessageBytes int) ([]entity.GraphChunk, error) {
	targetNamesMapping := make(map[string]int32, len(graph.Targets))
	for i, t := range graph.Targets {
		targetNamesMapping[t.Name] = int32(i + 1)
	}

	ruleTypeMapper := idmapper.NewMapper()
	tagMapper := idmapper.NewMapper()
	attrNameMapper := idmapper.NewMapper()
	attrStrValMapper := idmapper.NewMapper()

	protoTargets := make([]*tangopb.OptimizedTarget, 0, len(graph.Targets))
	idTargets := make([]entity.IDTarget, 0, len(graph.Targets))
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

		id := targetNamesMapping[t.Name]
		idt := entity.IDTarget{
			ID:                 id,
			Hash:               t.Hash,
			DirectDependencies: depIDs,
			Root:               t.Root,
			External:           t.External,
		}

		if t.RuleType != "" {
			idt.RuleType = ruleTypeMapper.ID(t.RuleType)
		}
		if len(t.Tags) > 0 {
			tagIDs := make([]int32, 0, len(t.Tags))
			for _, tag := range t.Tags {
				tagIDs = append(tagIDs, tagMapper.ID(tag))
			}
			idt.Tags = tagIDs
		}
		if len(t.Attributes) > 0 {
			attrs := make(map[int32]int32, len(t.Attributes))
			for k, v := range t.Attributes {
				attrs[attrNameMapper.ID(k)] = attrStrValMapper.ID(v)
			}
			idt.Attributes = attrs
		}

		idTargets = append(idTargets, idt)
		protoTargets = append(protoTargets, idTargetToProtoTarget(&idt))
	}

	targetIDToName := make(map[int32]string, len(targetNamesMapping))
	for s, id := range targetNamesMapping {
		targetIDToName[id] = s
	}

	// Use proto Size() for accurate splitting, then build entity chunks.
	targetGroups, err := streaming.SplitBySize(protoTargets, maxMessageBytes)
	if err != nil {
		return nil, err
	}

	var chunks []entity.GraphChunk
	idx := 0
	for _, g := range targetGroups {
		chunk := entity.GraphChunk{
			Targets: idTargets[idx : idx+len(g)],
		}
		idx += len(g)
		chunks = append(chunks, chunk)
	}

	metaGroups, err := streaming.SplitMetadata(
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
	for _, meta := range metaGroups {
		chunks = append(chunks, entity.GraphChunk{
			Metadata: meta,
		})
	}

	return chunks, nil
}

// TargetGraphToProto converts an entity.TargetGraph into chunked
// GetTargetGraphResponse messages ready for streaming. Each message is
// bounded by maxMessageBytes of real wire size.
func TargetGraphToProto(ctx context.Context, graph entity.TargetGraph, maxMessageBytes int) ([]*tangopb.GetTargetGraphResponse, error) {
	chunks, err := TargetGraphToChunks(ctx, graph, maxMessageBytes)
	if err != nil {
		return nil, err
	}
	responses := make([]*tangopb.GetTargetGraphResponse, 0, len(chunks))
	for i := range chunks {
		responses = append(responses, GraphChunkToProto(&chunks[i]))
	}
	return responses, nil
}

// GraphChunkToProto converts an entity.GraphChunk to the corresponding
// proto GetTargetGraphResponse.
func GraphChunkToProto(chunk *entity.GraphChunk) *tangopb.GetTargetGraphResponse {
	if chunk.Metadata != nil {
		return &tangopb.GetTargetGraphResponse{
			Item: &tangopb.GetTargetGraphResponse_Metadata{
				Metadata: entityMetadataToProto(chunk.Metadata),
			},
		}
	}
	targets := make([]*tangopb.OptimizedTarget, len(chunk.Targets))
	for i := range chunk.Targets {
		targets[i] = idTargetToProtoTarget(&chunk.Targets[i])
	}
	return &tangopb.GetTargetGraphResponse{
		Item: &tangopb.GetTargetGraphResponse_Targets{
			Targets: &tangopb.OptimizedTargets{Targets: targets},
		},
	}
}

// ProtoToGraphChunk converts a proto GetTargetGraphResponse to an
// entity.GraphChunk.
func ProtoToGraphChunk(resp *tangopb.GetTargetGraphResponse) entity.GraphChunk {
	switch item := resp.GetItem().(type) {
	case *tangopb.GetTargetGraphResponse_Targets:
		targets := make([]entity.IDTarget, len(item.Targets.GetTargets()))
		for i, t := range item.Targets.GetTargets() {
			targets[i] = protoTargetToIDTarget(t)
		}
		return entity.GraphChunk{Targets: targets}
	case *tangopb.GetTargetGraphResponse_Metadata:
		return entity.GraphChunk{Metadata: protoMetadataToEntity(item.Metadata)}
	default:
		return entity.GraphChunk{}
	}
}

func idTargetToProtoTarget(t *entity.IDTarget) *tangopb.OptimizedTarget {
	return &tangopb.OptimizedTarget{
		Id:                 t.ID,
		Hash:               t.Hash,
		DirectDependencies: t.DirectDependencies,
		RuleType:           t.RuleType,
		Tags:               t.Tags,
		Root:               t.Root,
		External:           t.External,
		Attributes:         t.Attributes,
	}
}

func protoTargetToIDTarget(t *tangopb.OptimizedTarget) entity.IDTarget {
	return entity.IDTarget{
		ID:                 t.GetId(),
		Hash:               t.GetHash(),
		DirectDependencies: t.GetDirectDependencies(),
		RuleType:           t.GetRuleType(),
		Tags:               t.GetTags(),
		Root:               t.GetRoot(),
		External:           t.GetExternal(),
		Attributes:         t.GetAttributes(),
	}
}

func entityMetadataToProto(m *entity.GraphMetadata) *tangopb.Metadata {
	return &tangopb.Metadata{
		TargetIdMapping:             m.TargetIDMapping,
		RuleTypeMapping:             m.RuleTypeMapping,
		TagMapping:                  m.TagMapping,
		AttributeNameMapping:        m.AttributeNameMapping,
		AttributeStringValueMapping: m.AttributeStringValueMapping,
	}
}

func protoMetadataToEntity(m *tangopb.Metadata) *entity.GraphMetadata {
	return &entity.GraphMetadata{
		TargetIDMapping:             m.GetTargetIdMapping(),
		RuleTypeMapping:             m.GetRuleTypeMapping(),
		TagMapping:                  m.GetTagMapping(),
		AttributeNameMapping:        m.GetAttributeNameMapping(),
		AttributeStringValueMapping: m.GetAttributeStringValueMapping(),
	}
}
