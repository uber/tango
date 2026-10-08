package mapper

import (
	"errors"
	"fmt"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/tangopb"
)

// ProtoToGetChangedTargetsRequest converts a proto GetChangedTargetsRequest to
// the domain type. It returns an error if either revision is invalid or the
// remotes differ. OutputConfig is output-only and is not mapped.
func ProtoToGetChangedTargetsRequest(req *tangopb.GetChangedTargetsRequest) (entity.GetChangedTargetsRequest, error) {
	if req == nil {
		return entity.GetChangedTargetsRequest{}, errors.New("get changed targets request is required")
	}
	first, err := ProtoToBuildDescription(req.GetFirstRevision())
	if err != nil {
		return entity.GetChangedTargetsRequest{}, fmt.Errorf("first revision: %w", err)
	}
	second, err := ProtoToBuildDescription(req.GetSecondRevision())
	if err != nil {
		return entity.GetChangedTargetsRequest{}, fmt.Errorf("second revision: %w", err)
	}
	if first.Remote != second.Remote {
		return entity.GetChangedTargetsRequest{}, errors.New("first and second revision must have the same remote")
	}
	return entity.GetChangedTargetsRequest{
		First:             first,
		Second:            second,
		ExcludeFilesRegex: req.GetRequestOptions().GetExtraExcludeFilesRegex(),
		BypassCache:       req.GetBypassCache(),
	}, nil
}

// ChangedTargetsResponseToProto converts an entity.GetChangedTargetsResponse
// to its proto equivalent for gRPC streaming.
func ChangedTargetsResponseToProto(resp *entity.GetChangedTargetsResponse) *tangopb.GetChangedTargetsResponse {
	if resp.Metadata != nil {
		return &tangopb.GetChangedTargetsResponse{
			Item: &tangopb.GetChangedTargetsResponse_Metadata{
				Metadata: metadataToProto(resp.Metadata),
			},
		}
	}
	changed := make([]*tangopb.ChangedTarget, len(resp.ChangedTargets))
	for i := range resp.ChangedTargets {
		ct := &resp.ChangedTargets[i]
		changed[i] = &tangopb.ChangedTarget{
			ChangeType: tangopb.ChangeType(int32(ct.ChangeType)),
			OldTarget:  optionalTargetToProto(ct.OldTarget),
			NewTarget:  optionalTargetToProto(ct.NewTarget),
			Distance:   ct.Distance,
		}
	}
	return &tangopb.GetChangedTargetsResponse{
		Item: &tangopb.GetChangedTargetsResponse_ChangedTargets{
			ChangedTargets: &tangopb.ChangedTargets{ChangedTargets: changed},
		},
	}
}

func optionalTargetToProto(t *entity.OptimizedTarget) *tangopb.OptimizedTarget {
	if t == nil {
		return nil
	}
	return optimizedTargetToProto(t)
}
