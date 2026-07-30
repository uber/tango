package entity

// ChangeRequest describes a single change (PR, diff) to layer on top of a
// base revision.
type ChangeRequest struct {
	// URL identifies the change request. Its format is interpreted by the
	// Orchestrator implementation that consumes it: the bundled native
	// orchestrator requires a canonical change URI of the form
	// github://{host[:port]}/{org}/{repo}/pull/{pr}/{head_sha}, per the
	// change-URI RFC
	// (https://github.com/uber/submitqueue/blob/main/doc/rfc/change-uri.md),
	// whose embedded head SHA pins the exact code state; custom
	// orchestrators may accept formats of their own. The URL participates
	// in cache keys as an opaque string.
	URL string
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
