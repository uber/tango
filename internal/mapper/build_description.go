package mapper

import (
	"github.com/uber/tango/entity"
	"github.com/uber/tango/tangopb"
)

// ToBuildDescription converts a proto BuildDescription pointer to the domain type.
// A nil proto value produces a zero-value BuildDescription.
func ToBuildDescription(desc *tangopb.BuildDescription) entity.BuildDescription {
	if desc == nil {
		return entity.BuildDescription{}
	}
	return entity.BuildDescription{
		Remote:   desc.GetRemote(),
		BaseSha:  desc.GetBaseSha(),
		Requests: toChangeRequests(desc.GetRequests()),
		Strategy: toComputationStrategy(desc.GetStrategy()),
	}
}

// toChangeRequests converts a slice of proto Request pointers to domain ChangeRequests.
func toChangeRequests(requests []*tangopb.Request) []entity.ChangeRequest {
	if len(requests) == 0 {
		return nil
	}
	out := make([]entity.ChangeRequest, len(requests))
	for i, r := range requests {
		out[i] = entity.ChangeRequest{
			URL:    r.GetUrl(),
			Commit: r.GetCommit(),
		}
	}
	return out
}

// toComputationStrategy converts a proto ComputationStrategy to the domain type.
func toComputationStrategy(s tangopb.ComputationStrategy) entity.ComputationStrategy {
	switch s {
	case tangopb.COMPUTATION_STRATEGY_UNSET:
		return entity.ComputationStrategyUnset
	case tangopb.COMPUTATION_STRATEGY_SHELL:
		return entity.ComputationStrategyShell
	case tangopb.COMPUTATION_STRATEGY_NATIVE:
		return entity.ComputationStrategyNative
	default:
		return entity.ComputationStrategyInvalid
	}
}
