package errors

const (
	// CancelCheckInterval is how often we poll ctx.Err() inside per-target hot loops.
	// Picked to keep overhead negligible while still surfacing cancellation in <100ms
	// for typical target rates.
	CancelCheckInterval = 1024
)
