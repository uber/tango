package entity

// ChangedTarget represents a target that differs between two revisions.
type ChangedTarget struct {
	ChangeType ChangeType       `json:"change_type"`
	OldTarget  *OptimizedTarget `json:"old_target,omitempty"`
	NewTarget  *OptimizedTarget `json:"new_target,omitempty"`
	Distance   int32            `json:"distance"`
}

// Size returns an estimate of the protobuf wire size for this changed target.
func (ct *ChangedTarget) Size() int {
	n := 0
	if ct.ChangeType != 0 {
		n += 1 + varintSize(uint64(ct.ChangeType))
	}
	if ct.OldTarget != nil {
		inner := ct.OldTarget.Size()
		n += 1 + varintSize(uint64(inner)) + inner
	}
	if ct.NewTarget != nil {
		inner := ct.NewTarget.Size()
		n += 1 + varintSize(uint64(inner)) + inner
	}
	if ct.Distance != 0 {
		n += 1 + varintSize(uint64(ct.Distance))
	}
	return n
}

// GetChangedTargetsResponse is one piece of a streamed changed-targets
// result — either a batch of changed targets or a metadata mapping.
// Exactly one field is non-nil.
type GetChangedTargetsResponse struct {
	ChangedTargets []ChangedTarget `json:"changed_targets"`
	Metadata       *Metadata       `json:"metadata,omitempty"`
}
