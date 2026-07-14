package mapper

import (
	"errors"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/tangopb"
)

// ProtoToBuildDescription converts a proto BuildDescription to the domain
// type. Returns an error if desc is nil or missing a required field (remote,
// base_sha) — every downstream consumer (cache-key derivation, workspace
// checkout) depends on both being set.
func ProtoToBuildDescription(desc *tangopb.BuildDescription) (entity.BuildDescription, error) {
	if desc == nil {
		return entity.BuildDescription{}, errors.New("build description is required")
	}
	if desc.GetRemote() == "" {
		return entity.BuildDescription{}, errors.New("build description remote is required")
	}
	if desc.GetBaseSha() == "" {
		return entity.BuildDescription{}, errors.New("build description base_sha is required")
	}
	return entity.BuildDescription{
		Remote:         desc.GetRemote(),
		BaseSha:        desc.GetBaseSha(),
		ChangeRequests: toChangeRequests(desc.GetRequests()),
		Strategy:       toComputationStrategy(desc.GetStrategy()),
	}, nil
}

// toChangeRequests converts a slice of proto Request to domain ChangeRequests.
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

// toComputationStrategy converts a proto ComputationStrategy to the domain ComputationStrategy.
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
