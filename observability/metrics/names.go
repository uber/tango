package metrics

// Operation names — the <op> subscope for Tango's core operations.
const (
	OpGetTargetGraph        = "get_target_graph"
	OpGetChangedTargets     = "get_changed_targets"
	OpGetChangedTargetGraph = "get_changed_target_graph"
	OpGetGraph              = "get_graph"
	OpCompareTargetGraphs   = "compare_target_graphs"
	OpGraphRunner           = "graph_runner"
)

// Tag keys.
const (
	TagRepo      = "repo"
	TagResult    = "result"
	TagOperation = "operation"
)

// Result values for TagResult.
const (
	ResultSuccess   = "success"
	ResultFailure   = "failure"
	ResultCancelled = "cancelled"
	ResultHit       = "hit"
	ResultMiss      = "miss"
)
