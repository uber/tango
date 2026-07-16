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

// IDTarget is the compact, ID-mapped representation of a target used for
// streaming and storage. String fields are replaced with int32 IDs that
// reference the accompanying GraphMetadata maps.
type IDTarget struct {
	ID                 int32
	Hash               string
	DirectDependencies []int32
	RuleType           int32
	Tags               []int32
	Root               bool
	External           bool
	Attributes         map[int32]int32
}

// GraphMetadata holds the ID-to-string mappings that accompany a set of
// IDTarget entries. Consumers merge metadata across chunks before
// resolving IDs.
type GraphMetadata struct {
	TargetIDMapping             map[int32]string
	RuleTypeMapping             map[int32]string
	TagMapping                  map[int32]string
	AttributeNameMapping        map[int32]string
	AttributeStringValueMapping map[int32]string
}

// GraphChunk is one piece of a streamed target graph — either a batch of
// ID-mapped targets or a metadata mapping. Exactly one field is non-nil.
type GraphChunk struct {
	Targets  []IDTarget
	Metadata *GraphMetadata
}
