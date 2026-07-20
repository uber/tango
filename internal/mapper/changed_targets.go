package mapper

import (
	"github.com/uber/tango/entity"
	"github.com/uber/tango/tangopb"
)

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
