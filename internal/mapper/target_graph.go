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

// ResultToTargetGraph converts a targethasher.Result into ID-mapped entity
// types. Returns a flat list of targets and their accompanying metadata.
// No chunking or proto conversion is performed.
func ResultToTargetGraph(ctx context.Context, result targethasher.Result) ([]entity.OptimizedTarget, *entity.Metadata, error) {
	targetNamesMapping := make(map[string]int32, len(result.TargetNames))
	for i, name := range result.TargetNames {
		targetNamesMapping[name] = int32(i + 1)
	}

	ruleTypeMapper := idmapper.NewMapper()
	tagMapper := idmapper.NewMapper()
	attrNameMapper := idmapper.NewMapper()
	attrStrValMapper := idmapper.NewMapper()

	targets := make([]entity.OptimizedTarget, 0, len(result.TargetNames))

	n := 0
	for _, name := range result.TargetNames {
		t, ok := result.Targets[name]
		if !ok {
			continue
		}
		if n%cancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
		}
		n++

		depIDs := make([]int32, 0, len(t.Deps))
		for _, depName := range t.Deps {
			if depID, ok := targetNamesMapping[depName]; ok {
				depIDs = append(depIDs, depID)
			}
		}

		idt := entity.OptimizedTarget{
			ID:                 targetNamesMapping[name],
			Hash:               hex.EncodeToString(t.Hash),
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
			for _, attr := range t.Attributes {
				if attr.GetType() == buildpb.Attribute_STRING && attr.Name != nil && attr.StringValue != nil {
					attrs[attrNameMapper.ID(*attr.Name)] = attrStrValMapper.ID(*attr.StringValue)
				}
			}
			if len(attrs) > 0 {
				idt.Attributes = attrs
			}
		}

		targets = append(targets, idt)
	}

	targetIDToName := make(map[int32]string, len(targetNamesMapping))
	for s, id := range targetNamesMapping {
		targetIDToName[id] = s
	}

	meta := &entity.Metadata{
		TargetIDMapping:             targetIDToName,
		RuleTypeMapping:             ruleTypeMapper.Invert(),
		TagMapping:                  tagMapper.Invert(),
		AttributeNameMapping:        attrNameMapper.Invert(),
		AttributeStringValueMapping: attrStrValMapper.Invert(),
	}

	return targets, meta, nil
}

// ChunkTargetGraph splits targets and metadata into wire-safe
// entity.GetTargetGraphResponse chunks bounded by maxMessageBytes.
func ChunkTargetGraph(targets []entity.OptimizedTarget, meta *entity.Metadata, maxMessageBytes int) ([]entity.GetTargetGraphResponse, error) {
	protoTargets := make([]*tangopb.OptimizedTarget, len(targets))
	for i := range targets {
		protoTargets[i] = optimizedTargetToProto(&targets[i])
	}

	targetGroups, err := streaming.SplitBySize(protoTargets, maxMessageBytes)
	if err != nil {
		return nil, err
	}

	var chunks []entity.GetTargetGraphResponse
	idx := 0
	for _, g := range targetGroups {
		chunks = append(chunks, entity.GetTargetGraphResponse{
			Targets: targets[idx : idx+len(g)],
		})
		idx += len(g)
	}

	metaGroups, err := streaming.SplitMetadata(
		meta.TargetIDMapping,
		meta.RuleTypeMapping,
		meta.TagMapping,
		meta.AttributeNameMapping,
		meta.AttributeStringValueMapping,
		maxMessageBytes,
	)
	if err != nil {
		return nil, err
	}
	for _, m := range metaGroups {
		chunks = append(chunks, entity.GetTargetGraphResponse{Metadata: m})
	}

	return chunks, nil
}

// GetTargetGraphResponseToProto converts an entity.GetTargetGraphResponse to
// the corresponding proto GetTargetGraphResponse.
func GetTargetGraphResponseToProto(chunk *entity.GetTargetGraphResponse) *tangopb.GetTargetGraphResponse {
	if chunk.Metadata != nil {
		return &tangopb.GetTargetGraphResponse{
			Item: &tangopb.GetTargetGraphResponse_Metadata{
				Metadata: metadataToProto(chunk.Metadata),
			},
		}
	}
	targets := make([]*tangopb.OptimizedTarget, len(chunk.Targets))
	for i := range chunk.Targets {
		targets[i] = optimizedTargetToProto(&chunk.Targets[i])
	}
	return &tangopb.GetTargetGraphResponse{
		Item: &tangopb.GetTargetGraphResponse_Targets{
			Targets: &tangopb.OptimizedTargets{Targets: targets},
		},
	}
}

// metadataToProto converts an entity.Metadata to a proto Metadata.
func metadataToProto(m *entity.Metadata) *tangopb.Metadata {
	return &tangopb.Metadata{
		TargetIdMapping:             m.TargetIDMapping,
		RuleTypeMapping:             m.RuleTypeMapping,
		TagMapping:                  m.TagMapping,
		AttributeNameMapping:        m.AttributeNameMapping,
		AttributeStringValueMapping: m.AttributeStringValueMapping,
	}
}

func optimizedTargetToProto(t *entity.OptimizedTarget) *tangopb.OptimizedTarget {
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

func protoToOptimizedTarget(t *tangopb.OptimizedTarget) entity.OptimizedTarget {
	return entity.OptimizedTarget{
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

func protoToMetadata(m *tangopb.Metadata) *entity.Metadata {
	return &entity.Metadata{
		TargetIDMapping:             m.GetTargetIdMapping(),
		RuleTypeMapping:             m.GetRuleTypeMapping(),
		TagMapping:                  m.GetTagMapping(),
		AttributeNameMapping:        m.GetAttributeNameMapping(),
		AttributeStringValueMapping: m.GetAttributeStringValueMapping(),
	}
}
