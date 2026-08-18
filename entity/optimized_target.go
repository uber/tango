package entity

import "github.com/uber/tango/internal/streaming/wire"

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

// Size returns an estimate of the protobuf wire size for this target.
// Used by streaming splitters to stay within gRPC message size limits.
func (t OptimizedTarget) Size() int {
	n := 0
	if t.ID != 0 {
		n += 1 + wire.VarintSize(uint64(t.ID))
	}
	if len(t.Hash) > 0 {
		n += 1 + wire.VarintSize(uint64(len(t.Hash))) + len(t.Hash)
	}
	if len(t.DirectDependencies) > 0 {
		dataSize := 0
		for _, d := range t.DirectDependencies {
			dataSize += wire.VarintSize(uint64(d))
		}
		n += 1 + wire.VarintSize(uint64(dataSize)) + dataSize
	}
	if t.RuleType != 0 {
		n += 1 + wire.VarintSize(uint64(t.RuleType))
	}
	if len(t.Tags) > 0 {
		dataSize := 0
		for _, tag := range t.Tags {
			dataSize += wire.VarintSize(uint64(tag))
		}
		n += 1 + wire.VarintSize(uint64(dataSize)) + dataSize
	}
	if t.Root {
		n += 1 + 1
	}
	if t.External {
		n += 1 + 1
	}
	for k, v := range t.Attributes {
		entrySize := 1 + wire.VarintSize(uint64(k)) + 1 + wire.VarintSize(uint64(v))
		n += 1 + wire.VarintSize(uint64(entrySize)) + entrySize
	}
	return n
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
	// AllTargetsFileHashes maps repo-relative file paths to their
	// hex-encoded content hashes for files listed in
	// RepositoryConfig.AllTargetsFiles. Nil when the repository has no
	// AllTargetsFiles configured, or when the graph was stored in a format
	// that predates this field (e.g. TGB).
	AllTargetsFileHashes map[string]string `json:"all_targets_file_hashes,omitempty"`
}

// GetTargetGraphResponse is one piece of a streamed target graph — either a
// batch of ID-mapped targets or a metadata mapping. Exactly one field is
// non-nil.
type GetTargetGraphResponse struct {
	Targets  []OptimizedTarget `json:"targets"`
	Metadata *Metadata         `json:"metadata,omitempty"`
}
