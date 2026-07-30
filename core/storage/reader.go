// Copyright (c) 2026 Uber Technologies, Inc.
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

// reader streams JSON-encoded values of type T from storage, supporting both
// versioned streams (header + data records + footer) and legacy streams
// (bare data records without framing). For versioned streams the footer is
// verified on EOF; for legacy streams the reader trusts the physical EOF.
type reader[T any] struct {
	rc  io.ReadCloser
	dec *json.Decoder

	// versioned is true when the stream started with a header record.
	versioned bool
	// recordCount tracks how many data records have been returned.
	recordCount int
	// pending holds a data record decoded while probing for the header.
	// It is returned on the first call to Read before resuming normal
	// decoding.
	pending *T
	// done is set once the footer has been verified (versioned) or EOF
	// reached (legacy).
	done bool
}

// Read decodes the next value from the stream. Returns io.EOF at end of
// stream. For versioned streams, the footer is validated before returning
// io.EOF; a missing or mismatched footer returns an error wrapping
// ErrStreamCorrupted.
func (r *reader[T]) Read() (T, error) {
	var zero T
	if r.done {
		return zero, io.EOF
	}

	// Return a buffered first record from legacy-probe.
	if r.pending != nil {
		v := *r.pending
		r.pending = nil
		r.recordCount++
		return v, nil
	}

	// Decode the next raw JSON token.
	var raw json.RawMessage
	if err := r.dec.Decode(&raw); err != nil {
		if err == io.EOF {
			if r.versioned {
				return zero, newCorruptedError("unexpected EOF: missing footer")
			}
			r.done = true
			return zero, io.EOF
		}
		return zero, err
	}

	// Check if the record is a footer.
	if env, ok := parseEnvelope(raw); ok {
		if env.Footer != nil {
			if !r.versioned {
				return zero, newCorruptedError("footer in unversioned stream")
			}
			if env.Footer.RecordCount != r.recordCount {
				return zero, newCorruptedError("footer record count mismatch")
			}
			r.done = true
			return zero, io.EOF
		}
		// A header mid-stream is unexpected.
		return zero, newCorruptedError("unexpected header record mid-stream")
	}

	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return zero, err
	}
	r.recordCount++
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
// JSON-encoded T values from it. It probes the first record to detect
// whether the stream is versioned (starts with a header) or legacy.
func newReader[T any](ctx context.Context, st Storage, key string) (*reader[T], error) {
	resp, err := st.Get(ctx, DownloadRequest{Key: key})
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(resp.ReadCloser)
	r := &reader[T]{
		rc:  resp.ReadCloser,
		dec: dec,
	}

	// Probe the first record to see if it is a header.
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		if err == io.EOF {
			// Empty blob — treat as legacy empty stream.
			r.done = true
			return r, nil
		}
		resp.ReadCloser.Close()
		return nil, err
	}

	if env, ok := parseEnvelope(raw); ok && env.Header != nil {
		r.versioned = true
		return r, nil
	}

	// First record is a data record (legacy stream). Decode it and stash.
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		resp.ReadCloser.Close()
		return nil, err
	}
	r.pending = &v
	return r, nil
}
