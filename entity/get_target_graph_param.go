package entity

// RequestOptions carries request-scoped inputs that affect graph computation.
// Distinct from output configuration, which only shapes the response.
type RequestOptions struct {
	ExtraExcludeFilesRegex []string
}

// GetTargetGraphParam is the input to the Orchestrator's GetTargetGraph method.
// OutputConfig is deliberately excluded: it is a transport/presentation concern
// that must not reach the orchestrator, where it could poison the shared cache
// with stripped graphs.
type GetTargetGraphParam struct {
	Build          BuildDescription
	RequestOptions RequestOptions
	BypassCache    bool
}
