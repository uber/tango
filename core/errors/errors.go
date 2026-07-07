package errors

const (
	// CancelCheckInterval is how often we poll ctx.Err() inside per-target hot loops.
	// Picked to keep overhead negligible while still surfacing cancellation in <100ms
	// for typical target rates.
	CancelCheckInterval = 4096

	// DefaultTargetChunkSize is the default number of OptimizedTarget entries per stream message.
	// Sized conservatively: at ~40KB/target worst-case (target with ~10K direct deps × 4 bytes),
	// 250 targets ≈ 10MB — well under the 64MB default gRPC per-message limit.
	DefaultTargetChunkSize = 250

	// DefaultChangedTargetChunkSize is the default number of ChangedTarget entries per stream message.
	// A ChangedTarget carries both old_target and new_target (2× an OptimizedTarget), so we use
	// half the regular chunk size to stay within the same byte budget.
	DefaultChangedTargetChunkSize = 125

	// DefaultMetadataMapChunkSize is the max entries per metadata message chunk.
	// target_id_mapping and attribute_string_value_mapping scale with repo size and can exceed
	// the 64MB gRPC message limit for large monorepos, so they are split across multiple messages.
	// At ~85 bytes/entry (60-char avg target name + proto overhead), 50 000 entries ≈ 4.25MB per chunk.
	DefaultMetadataMapChunkSize = 50_000
)
