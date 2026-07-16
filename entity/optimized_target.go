package entity

// OptimizedTarget is the domain representation of a single build target
// with its hash, dependencies, and metadata. All references use string
// names — ID assignment is a serialization concern handled by the mapper.
type OptimizedTarget struct {
	Name         string
	Hash         string
	Dependencies []string
	RuleType     string
	Tags         []string
	Root         bool
	External     bool
	Attributes   map[string]string
}

// TargetGraph is the domain representation of a complete target graph:
// a topologically sorted list of optimized targets.
type TargetGraph struct {
	Targets []OptimizedTarget
}
