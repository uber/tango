package entity

// ChangeRequest describes a single change (PR, diff) to layer on top of a
// base revision.
type ChangeRequest struct {
	// URL identifies the change request (PR/diff link) being layered on.
	URL string
	// Commit is the SHA to apply on top of the base revision.
	Commit string
}

// BuildDescription identifies a repository state: a base revision plus
// optional change requests layered on top.
type BuildDescription struct {
	// Remote is the repository to check out, e.g. a gitolite/GitHub remote URL.
	Remote string
	// BaseSha is the base commit the workspace is checked out to before
	// applying ChangeRequests.
	BaseSha string
	// ChangeRequests are applied on top of BaseSha, in order, to materialize
	// the workspace whose treehash keys the cache.
	ChangeRequests []ChangeRequest
	// Strategy selects which graph computation method to use.
	Strategy ComputationStrategy
}
