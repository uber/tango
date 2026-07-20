package entity

// ChangedTarget represents a target that differs between two revisions.
type ChangedTarget struct {
	ChangeType ChangeType       `json:"change_type"`
	OldTarget  *OptimizedTarget `json:"old_target,omitempty"`
	NewTarget  *OptimizedTarget `json:"new_target,omitempty"`
	Distance   int32            `json:"distance"`
}

// GetChangedTargetsResponse is one piece of a streamed changed-targets
// result — either a batch of changed targets or a metadata mapping.
// Exactly one field is non-nil.
type GetChangedTargetsResponse struct {
	ChangedTargets []ChangedTarget `json:"changed_targets"`
	Metadata       *Metadata       `json:"metadata,omitempty"`
}
