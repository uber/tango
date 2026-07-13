package entity

// GetTargetGraphRequest is the input to the Orchestrator's GetTargetGraph method.
type GetTargetGraphRequest struct {
	// Build identifies the repository state to compute the graph for.
	Build BuildDescription
	// ExcludeFilesRegex are additional file-path regexes to exclude when
	// computing target hashes.
	ExcludeFilesRegex []string
	// BypassCache, when true, skips the cache read and recomputes the graph,
	// overwriting the existing cached result.
	BypassCache bool
}
