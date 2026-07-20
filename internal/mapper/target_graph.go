package mapper

import (
	"errors"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/tangopb"
)

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
