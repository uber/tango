package mapper

import (
	"context"
	"io"

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

// GraphChunkReader reads entity.GraphChunk values from storage. It wraps
// storage.GraphReader and converts proto → entity on the fly.
type GraphChunkReader struct {
	inner storage.GraphReader
}

// NewGraphChunkReader opens a stored target graph at key and returns a
// reader that yields entity.GraphChunk values. Proto deserialization
// happens internally — callers see only entity types.
func NewGraphChunkReader(ctx context.Context, st storage.Storage, key string) (*GraphChunkReader, error) {
	gr, err := storage.NewGraphReader(ctx, st, key)
	if err != nil {
		return nil, err
	}
	return &GraphChunkReader{inner: gr}, nil
}

// Read returns the next GraphChunk. Returns io.EOF at the end of the stream.
func (r *GraphChunkReader) Read() (entity.GraphChunk, error) {
	resp, err := r.inner.Read()
	if err != nil {
		return entity.GraphChunk{}, err
	}
	return ProtoToGraphChunk(resp), nil
}

// Close releases the underlying reader.
func (r *GraphChunkReader) Close() error {
	return r.inner.Close()
}

// ReadAllGraphChunks reads all GraphChunks from storage at key into a slice.
func ReadAllGraphChunks(ctx context.Context, st storage.Storage, key string) ([]entity.GraphChunk, error) {
	r, err := NewGraphChunkReader(ctx, st, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var chunks []entity.GraphChunk
	for {
		chunk, err := r.Read()
		if err == io.EOF {
			return chunks, nil
		}
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
}
