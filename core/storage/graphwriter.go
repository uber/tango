// Copyright (c) 2025 Uber Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

import (
	"context"
	"encoding/gob"
	"fmt"
	"io"

	"github.com/uber/tango/entity"
)

// WriteGraphStream writes entity.GetTargetGraphResponse values to storage
// as a gob-encoded stream, allowing streaming reads via NewGraphReader.
func WriteGraphStream(ctx context.Context, st Storage, key string, chunks []entity.GetTargetGraphResponse) error {
	return writeStream(ctx, st, key, chunks)
}

// WriteChangedTargetsStream writes entity.GetChangedTargetsResponse values to storage
// as a gob-encoded stream, allowing streaming reads via NewChangedTargetsReader.
func WriteChangedTargetsStream(ctx context.Context, st Storage, key string, responses []entity.GetChangedTargetsResponse) error {
	return writeStream(ctx, st, key, responses)
}

// writeStream gob-encodes values and streams them to storage under key.
// It uses an io.Pipe so the serialized payload is never buffered in full:
// a writer goroutine encodes into the pipe while Put consumes from it.
func writeStream[T any](ctx context.Context, st Storage, key string, values []T) error {
	pr, pw := io.Pipe()
	writerErr := make(chan error, 1)
	go func() {
		enc := gob.NewEncoder(pw)
		var err error
		for i := range values {
			if err = context.Cause(ctx); err != nil {
				break
			}
			if err = enc.Encode(&values[i]); err != nil {
				err = fmt.Errorf("encode value: %w", err)
				break
			}
		}
		pw.CloseWithError(err)
		writerErr <- err
	}()
	putErr := st.Put(ctx, UploadRequest{Key: key, Reader: pr})
	pr.CloseWithError(putErr)
	writeErr := <-writerErr
	if putErr != nil {
		return putErr
	}
	return writeErr
}
