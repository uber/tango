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
	"encoding/json"
	"io"
)

// reader streams JSON-encoded values of type T from storage.
type reader[T any] struct {
	rc  io.ReadCloser
	dec *json.Decoder
}

// Read decodes the next value from the stream. Returns io.EOF at end of stream.
func (r *reader[T]) Read() (T, error) {
	var v T
	if err := r.dec.Decode(&v); err != nil {
		var zero T
		return zero, err
	}
	return v, nil
}

// Close releases the underlying reader.
func (r *reader[T]) Close() error {
	if r.rc != nil {
		return r.rc.Close()
	}
	return nil
}

// newReader opens the blob at key and returns a reader that decodes
// JSON-encoded T values from it.
func newReader[T any](ctx context.Context, st Storage, key string) (*reader[T], error) {
	resp, err := st.Get(ctx, DownloadRequest{Key: key})
	if err != nil {
		return nil, err
	}
	return &reader[T]{
		rc:  resp.ReadCloser,
		dec: json.NewDecoder(resp.ReadCloser),
	}, nil
}
