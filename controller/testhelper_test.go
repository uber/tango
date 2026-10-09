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

package controller

import (
	"bytes"
	"context"
	"encoding/gob"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uber/tango/config"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/observability/metrics"
	"go.uber.org/zap"
)

var testRepositoryConfig = config.RepositoryConfig{RepositoryID: "test-repository"}

func testRepositoryID(string) string {
	return "test-repository"
}

func newTestController(logger *zap.Logger) *controller {
	return &controller{
		logger:          logger,
		emitter:         metrics.Nop(),
		maxMessageBytes: config.DefaultMaxMessageBytes,
		appCtx:          context.Background(),
	}
}

type constantGraphConfig struct{ gc config.GraphConfig }

func (c constantGraphConfig) GetGraphConfig(string) (config.GraphConfig, error) { return c.gc, nil }

func staticGraphConfig(gc config.GraphConfig) config.GraphConfigProvider {
	return constantGraphConfig{gc: gc}
}

// newGraphReader builds a storage.GraphReader from entity chunks
// by writing JSON to in-memory storage and reading back.
func newGraphReader(t *testing.T, chunks ...entity.GetTargetGraphResponse) storage.GraphReader {
	t.Helper()
	st := storage.NewMemoryStorage()
	require.NoError(t, storage.WriteGraphStream(t.Context(), st, "test-graph", chunks))
	reader, err := storage.NewGraphReader(t.Context(), st, "test-graph")
	require.NoError(t, err)
	return reader
}

// readCloser wraps a string as an io.ReadCloser for storage mock responses.
func readCloser(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

// encodeGraphChunks gob-encodes a sequence of entity.GetTargetGraphResponse
// values the way storage.WriteGraphStream does, for use with storage mocks
// that need raw bytes rather than a real backing store.
func encodeGraphChunks(t *testing.T, chunks []entity.GetTargetGraphResponse) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	for i := range chunks {
		require.NoError(t, enc.Encode(&chunks[i]))
	}
	return buf.Bytes()
}

// encodeChangedTargetsChunks gob-encodes a sequence of
// entity.GetChangedTargetsResponse values the way
// storage.WriteChangedTargetsStream does, for use with storage mocks that
// need raw bytes rather than a real backing store.
func encodeChangedTargetsChunks(t *testing.T, chunks []entity.GetChangedTargetsResponse) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	for i := range chunks {
		require.NoError(t, enc.Encode(&chunks[i]))
	}
	return buf.Bytes()
}

// decodeChangedTargetsChunks reverses encodeChangedTargetsChunks, decoding a
// gob-encoded blob the way storage.NewChangedTargetsReader does, so a test
// can inspect exactly how many chunks a cache write produced.
func decodeChangedTargetsChunks(t *testing.T, data []byte) []entity.GetChangedTargetsResponse {
	t.Helper()
	dec := gob.NewDecoder(bytes.NewReader(data))
	var chunks []entity.GetChangedTargetsResponse
	for {
		var resp entity.GetChangedTargetsResponse
		err := dec.Decode(&resp)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		chunks = append(chunks, resp)
	}
	return chunks
}
