package entity

// GetTargetGraphRequest is the input to the Orchestrator's GetTargetGraph method.
// OutputConfig is deliberately excluded: it is a transport/presentation concern
// that must not reach the orchestrator, where it could poison the shared cache
// with stripped graphs.
type GetTargetGraphRequest struct {
	// Build identifies the repository state to compute the graph for.
	Build BuildDescription
	// ExcludeFilesRegex are additional file-path regexes to exclude when
	// computing target hashes, appended to the server-side repository config.
	ExcludeFilesRegex []string
	// BypassCache, when true, skips the cache read and recomputes the graph,
	// overwriting the existing cached result.
	BypassCache bool
}
