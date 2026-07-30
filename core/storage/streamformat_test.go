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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/entity"
)

func TestStreamRoundTrip(t *testing.T) {
	t.Run("graph stream round-trips with footer", func(t *testing.T) {
		st := NewMemoryStorage()
		ctx := context.Background()
		chunks := []entity.GetTargetGraphResponse{
			{Targets: []entity.OptimizedTarget{{ID: 1, Hash: "abc"}}},
			{Metadata: &entity.Metadata{TargetIDMapping: map[int32]string{1: "//a:a"}}},
		}
		require.NoError(t, WriteGraphStream(ctx, st, "g1", chunks))

		reader, err := NewGraphReader(ctx, st, "g1")
		require.NoError(t, err)
		defer reader.Close()

		var got []entity.GetTargetGraphResponse
		for {
			v, err := reader.Read()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			got = append(got, v)
		}
		assert.Len(t, got, 2)
		assert.Equal(t, int32(1), got[0].Targets[0].ID)
		assert.Equal(t, "//a:a", got[1].Metadata.TargetIDMapping[1])
	})

	t.Run("changed-targets stream round-trips with footer", func(t *testing.T) {
		st := NewMemoryStorage()
		ctx := context.Background()
		chunks := []entity.GetChangedTargetsResponse{
			{ChangedTargets: []entity.ChangedTarget{{Distance: 1}}},
			{Metadata: &entity.Metadata{}},
		}
		require.NoError(t, WriteChangedTargetsStream(ctx, st, "ct1", chunks))

		reader, err := NewChangedTargetsReader(ctx, st, "ct1")
		require.NoError(t, err)
		defer reader.Close()

		var got []entity.GetChangedTargetsResponse
		for {
			v, err := reader.Read()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			got = append(got, v)
		}
		assert.Len(t, got, 2)
		assert.Equal(t, int32(1), got[0].ChangedTargets[0].Distance)
	})

	t.Run("empty stream round-trips", func(t *testing.T) {
		st := NewMemoryStorage()
		ctx := context.Background()
		require.NoError(t, WriteGraphStream(ctx, st, "empty", nil))

		reader, err := NewGraphReader(ctx, st, "empty")
		require.NoError(t, err)
		defer reader.Close()

		_, err = reader.Read()
		assert.ErrorIs(t, err, io.EOF)
	})
}

func TestStreamTruncationDetection(t *testing.T) {
	t.Run("truncation after complete record is detected", func(t *testing.T) {
		// Write a valid versioned stream, then truncate after the first
		// data record (removing the second record and the footer).
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		require.NoError(t, encodeHeader(enc))
		require.NoError(t, enc.Encode(entity.GetTargetGraphResponse{
			Targets: []entity.OptimizedTarget{{ID: 1, Hash: "abc"}},
		}))
		require.NoError(t, enc.Encode(entity.GetTargetGraphResponse{
			Metadata: &entity.Metadata{},
		}))
		require.NoError(t, encodeFooter(enc, 2))

		// Keep only header + first data record (drop second record + footer).
		full := buf.Bytes()
		lines := bytes.SplitAfter(full, []byte("\n"))
		// lines[0]=header, lines[1]=first data, lines[2]=second data, lines[3]=footer
		require.True(t, len(lines) >= 4, "expected at least 4 lines, got %d", len(lines))
		truncated := bytes.Join(lines[:2], nil)

		st := NewMemoryStorage()
		ctx := context.Background()
		require.NoError(t, st.Put(ctx, UploadRequest{
			Key:    "trunc",
			Reader: bytes.NewReader(truncated),
		}))

		reader, err := NewGraphReader(ctx, st, "trunc")
		require.NoError(t, err)
		defer reader.Close()

		// First record reads fine.
		v, err := reader.Read()
		require.NoError(t, err)
		assert.Equal(t, int32(1), v.Targets[0].ID)

		// Next read should detect the missing footer.
		_, err = reader.Read()
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrStreamCorrupted), "expected ErrStreamCorrupted, got: %v", err)
	})

	t.Run("footer record count mismatch is detected", func(t *testing.T) {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		require.NoError(t, encodeHeader(enc))
		require.NoError(t, enc.Encode(entity.GetTargetGraphResponse{
			Targets: []entity.OptimizedTarget{{ID: 1, Hash: "abc"}},
		}))
		// Footer claims 5 records but only 1 was written.
		require.NoError(t, encodeFooter(enc, 5))

		st := NewMemoryStorage()
		ctx := context.Background()
		require.NoError(t, st.Put(ctx, UploadRequest{
			Key:    "mismatch",
			Reader: bytes.NewReader(buf.Bytes()),
		}))

		reader, err := NewGraphReader(ctx, st, "mismatch")
		require.NoError(t, err)
		defer reader.Close()

		_, err = reader.Read()
		require.NoError(t, err)

		_, err = reader.Read()
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrStreamCorrupted), "expected ErrStreamCorrupted, got: %v", err)
	})
}

func TestLegacyStreamBehavior(t *testing.T) {
	t.Run("legacy stream without header reads normally", func(t *testing.T) {
		// Simulate a legacy blob: plain newline-delimited JSON, no header/footer.
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.Encode(entity.GetTargetGraphResponse{
			Targets: []entity.OptimizedTarget{{ID: 1, Hash: "legacy"}},
		})
		enc.Encode(entity.GetTargetGraphResponse{
			Metadata: &entity.Metadata{TargetIDMapping: map[int32]string{1: "//legacy:a"}},
		})

		st := NewMemoryStorage()
		ctx := context.Background()
		require.NoError(t, st.Put(ctx, UploadRequest{
			Key:    "legacy",
			Reader: bytes.NewReader(buf.Bytes()),
		}))

		reader, err := NewGraphReader(ctx, st, "legacy")
		require.NoError(t, err)
		defer reader.Close()

		var got []entity.GetTargetGraphResponse
		for {
			v, err := reader.Read()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			got = append(got, v)
		}
		assert.Len(t, got, 2)
		assert.Equal(t, "legacy", got[0].Targets[0].Hash)
	})

	t.Run("legacy empty blob reads as empty stream", func(t *testing.T) {
		st := NewMemoryStorage()
		ctx := context.Background()
		require.NoError(t, st.Put(ctx, UploadRequest{
			Key:    "legacy-empty",
			Reader: bytes.NewReader(nil),
		}))

		reader, err := NewGraphReader(ctx, st, "legacy-empty")
		require.NoError(t, err)
		defer reader.Close()

		_, err = reader.Read()
		assert.ErrorIs(t, err, io.EOF)
	})

	t.Run("legacy truncated blob returns decode error not ErrStreamCorrupted", func(t *testing.T) {
		// A legacy blob truncated mid-record returns a JSON decode error,
		// not ErrStreamCorrupted (since there is no versioning to enforce).
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.Encode(entity.GetChangedTargetsResponse{ChangedTargets: []entity.ChangedTarget{}})
		enc.Encode(entity.GetChangedTargetsResponse{Metadata: &entity.Metadata{}})
		truncated := buf.Bytes()[:buf.Len()-5]

		st := NewMemoryStorage()
		ctx := context.Background()
		require.NoError(t, st.Put(ctx, UploadRequest{
			Key:    "legacy-trunc",
			Reader: bytes.NewReader(truncated),
		}))

		reader, err := NewChangedTargetsReader(ctx, st, "legacy-trunc")
		require.NoError(t, err)
		defer reader.Close()

		// First record succeeds.
		_, err = reader.Read()
		require.NoError(t, err)

		// Second record fails with a JSON error, not ErrStreamCorrupted.
		_, err = reader.Read()
		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrStreamCorrupted),
			"legacy truncation should not produce ErrStreamCorrupted")
	})
}
