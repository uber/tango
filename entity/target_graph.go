package entity

// GetTargetGraphRequest is the input to the Orchestrator's GetTargetGraph method.
// OutputConfig is deliberately excluded: it is a transport/presentation concern
// that must not reach the orchestrator, where it could poison the shared cache
// with stripped graphs.
type GetTargetGraphRequest struct {
	Build             BuildDescription
	ExcludeFilesRegex []string
	BypassCache       bool
}
