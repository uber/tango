package entity

// OptimizedTarget is the compact, ID-mapped representation of a target used
// for streaming and storage. String fields are replaced with int32 IDs that
// reference the accompanying Metadata maps.
type OptimizedTarget struct {
	ID                 int32           `json:"id"`
	Hash               string          `json:"hash"`
	DirectDependencies []int32         `json:"direct_dependencies"`
	RuleType           int32           `json:"rule_type"`
	Tags               []int32         `json:"tags"`
	Root               bool            `json:"root"`
	External           bool            `json:"external"`
	Attributes         map[int32]int32 `json:"attributes"`
}

// Metadata holds the ID-to-string mappings that accompany a set of
// OptimizedTarget entries. Consumers merge metadata across chunks before
// resolving IDs.
type Metadata struct {
	TargetIDMapping             map[int32]string `json:"target_id_mapping"`
	RuleTypeMapping             map[int32]string `json:"rule_type_mapping"`
	TagMapping                  map[int32]string `json:"tag_mapping"`
	AttributeNameMapping        map[int32]string `json:"attribute_name_mapping"`
	AttributeStringValueMapping map[int32]string `json:"attribute_string_value_mapping"`
}

// GetTargetGraphResponse is one piece of a streamed target graph — either a
// batch of ID-mapped targets or a metadata mapping. Exactly one field is
// non-nil.
type GetTargetGraphResponse struct {
	Targets  []OptimizedTarget `json:"targets"`
	Metadata *Metadata         `json:"metadata,omitempty"`
}
