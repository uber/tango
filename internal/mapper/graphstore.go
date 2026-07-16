package mapper

import (
	"context"

	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
)

// WriteTargetGraph converts an entity.TargetGraph to chunked proto
// responses and writes them to storage. This keeps proto conversion
// out of the orchestrator — callers only work with entity types.
func WriteTargetGraph(ctx context.Context, st storage.Storage, key string, graph entity.TargetGraph, maxMessageBytes int) error {
	responses, err := TargetGraphToProto(ctx, graph, maxMessageBytes)
	if err != nil {
		return err
	}
	return storage.WriteGraphStream(ctx, st, key, responses)
}
