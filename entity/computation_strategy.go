package entity

// ComputationStrategy controls which graph computation method to use.
type ComputationStrategy int

const (
	// ComputationStrategyUnset is the zero value: no strategy was specified
	// by the caller, so the orchestrator picks its own default.
	ComputationStrategyUnset ComputationStrategy = iota
	// ComputationStrategyInvalid marks an unrecognized or out-of-range
	// strategy; never set intentionally.
	ComputationStrategyInvalid
	// ComputationStrategyShell computes the graph by shelling out to Bazel.
	ComputationStrategyShell
	// ComputationStrategyNative computes the graph using the in-process
	// native graph runner.
	ComputationStrategyNative
)

// String returns the strategy name, matching the proto enum string
// representation used as a component in cache key paths.
func (s ComputationStrategy) String() string {
	switch s {
	case ComputationStrategyUnset:
		return "COMPUTATION_STRATEGY_UNSET"
	case ComputationStrategyShell:
		return "COMPUTATION_STRATEGY_SHELL"
	case ComputationStrategyNative:
		return "COMPUTATION_STRATEGY_NATIVE"
	default:
		return "COMPUTATION_STRATEGY_INVALID"
	}
}
