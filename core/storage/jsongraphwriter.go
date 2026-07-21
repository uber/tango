package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/uber/tango/entity"
)

type targetGraphBlob struct {
	Targets  []entity.OptimizedTarget `json:"targets"`
	Metadata *entity.Metadata         `json:"metadata,omitempty"`
}

// WriteGraphJSON writes targets and metadata as a single JSON blob to storage.
// Uses io.Pipe so the JSON encoding streams directly into Put without
// buffering the entire serialized payload in memory.
func WriteGraphJSON(ctx context.Context, st Storage, key string, targets []entity.OptimizedTarget, meta *entity.Metadata) error {
	pr, pw := io.Pipe()
	writerErr := make(chan error, 1)
	go func() {
		enc := json.NewEncoder(pw)
		t := targets
		if t == nil {
			t = []entity.OptimizedTarget{}
		}
		err := enc.Encode(targetGraphBlob{
			Targets:  t,
			Metadata: meta,
		})
		pw.CloseWithError(err)
		writerErr <- err
	}()

	putErr := st.Put(ctx, UploadRequest{Key: key, Reader: pr})
	pr.CloseWithError(putErr)
	writeErr := <-writerErr
	if putErr != nil {
		return fmt.Errorf("put graph: %w", putErr)
	}
	if writeErr != nil {
		return fmt.Errorf("encode graph: %w", writeErr)
	}
	return nil
}
