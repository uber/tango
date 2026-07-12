package entity

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
	Remote         string
	BaseSha        string
	ChangeRequests []ChangeRequest
	Strategy       ComputationStrategy
}
