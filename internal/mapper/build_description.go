package mapper

import (
	"errors"
	"fmt"

	tangoerrors "github.com/uber/tango/core/errors"
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
	strategy, err := validateComputationStrategy(desc.GetStrategy())
	if err != nil {
		return entity.BuildDescription{}, err
	}
	return entity.BuildDescription{
		Remote:         desc.GetRemote(),
		BaseSha:        desc.GetBaseSha(),
		ChangeRequests: toChangeRequests(desc.GetRequests()),
		Strategy:       strategy,
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

// validateComputationStrategy converts a proto ComputationStrategy to the
// domain type. Supported strategies are preserved so the orchestrator can
// select the requested runner; invalid or unknown values return a
// user-classified error.
func validateComputationStrategy(s tangopb.ComputationStrategy) (entity.ComputationStrategy, error) {
	switch s {
	case tangopb.COMPUTATION_STRATEGY_UNSET:
		return entity.ComputationStrategyUnset, nil
	case tangopb.COMPUTATION_STRATEGY_SHELL:
		return entity.ComputationStrategyShell, nil
	case tangopb.COMPUTATION_STRATEGY_NATIVE:
		return entity.ComputationStrategyNative, nil
	default:
		return entity.ComputationStrategyInvalid, tangoerrors.NewUser(
			fmt.Errorf("unknown computation strategy: %d", int32(s)),
		)
	}
}
