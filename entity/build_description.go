package entity

// ComputationStrategy controls which graph computation method to use.
type ComputationStrategy int

const (
	ComputationStrategyInvalid ComputationStrategy = iota
	ComputationStrategyUnset
	ComputationStrategyShell
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

// ChangeRequest describes a single change (PR, diff) to layer on top of a
// base revision. Named ChangeRequest (not Request) to avoid collision with
// workspace.Request.
type ChangeRequest struct {
	URL    string
	Commit string
}

// BuildDescription identifies a repository state: a base revision plus
// optional change requests layered on top.
type BuildDescription struct {
	Remote   string
	BaseSha  string
	Requests []ChangeRequest
	Strategy ComputationStrategy
}
