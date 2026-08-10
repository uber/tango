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

package storage_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
)

// tgbTestHash builds a deterministic 40-char hex hash distinct per seed.
func tgbTestHash(seed int) string {
	raw := make([]byte, 20)
	for i := range raw {
		raw[i] = byte(seed*31 + i*7 + 1)
	}
	return hex.EncodeToString(raw)
}

// tgbTestChunks builds a small chunked graph stream: two target chunks and a
// metadata chunk, the shape the producer emits.
func tgbTestChunks() []entity.GetTargetGraphResponse {
	return []entity.GetTargetGraphResponse{
		{Targets: []entity.OptimizedTarget{
			{ID: 1, Hash: tgbTestHash(1), RuleType: 100, Root: true},
			{ID: 2, Hash: tgbTestHash(2), RuleType: 300, DirectDependencies: []int32{1}},
		}},
		{Targets: []entity.OptimizedTarget{
			{ID: 3, Hash: tgbTestHash(3), RuleType: 100, Tags: []int32{7}, Attributes: map[int32]int32{5: 6}},
		}},
		{Metadata: &entity.Metadata{
			TargetIDMapping:             map[int32]string{1: "//app:lib", 2: "//app:src", 3: "//app/sub:lib"},
			RuleTypeMapping:             map[int32]string{100: "go_library", 300: "source file"},
			TagMapping:                  map[int32]string{7: "manual"},
			AttributeNameMapping:        map[int32]string{5: "visibility"},
			AttributeStringValueMapping: map[int32]string{6: "//visibility:public"},
		}},
	}
}

// TestTGBGraphRoundTrip writes a chunked graph as a TGB blob and reads it
// back through the chunked GraphReader interface, comparing the semantic
// content (labels, hashes, dep labels, tags, attributes) — chunk boundaries
// and int32 IDs are representation details the format does not preserve.
func TestTGBGraphRoundTrip(t *testing.T) {
	st := storage.NewMemoryStorage()
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st, "graph-tgb", tgbTestChunks()))

	reader, err := storage.NewTGBGraphReader(t.Context(), st, "graph-tgb", 1<<20)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	// The random-access form is available without any decode.
	require.Equal(t, 3, reader.TGB().NodeCount())

	var chunks []entity.GetTargetGraphResponse
	for {
		chunk, err := reader.Read()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		chunks = append(chunks, chunk)
	}

	// Merge decoded chunks and resolve to a comparable semantic view.
	type semTarget struct {
		hash, ruleType string
		deps, tags     []string
		attrs          map[string]string
		root, external bool
	}
	sem := make(map[string]semTarget)
	meta := &entity.Metadata{
		TargetIDMapping:             map[int32]string{},
		RuleTypeMapping:             map[int32]string{},
		TagMapping:                  map[int32]string{},
		AttributeNameMapping:        map[int32]string{},
		AttributeStringValueMapping: map[int32]string{},
	}
	var targets []entity.OptimizedTarget
	for _, c := range chunks {
		targets = append(targets, c.Targets...)
		if m := c.Metadata; m != nil {
			for k, v := range m.TargetIDMapping {
				meta.TargetIDMapping[k] = v
			}
			for k, v := range m.RuleTypeMapping {
				meta.RuleTypeMapping[k] = v
			}
			for k, v := range m.TagMapping {
				meta.TagMapping[k] = v
			}
			for k, v := range m.AttributeNameMapping {
				meta.AttributeNameMapping[k] = v
			}
			for k, v := range m.AttributeStringValueMapping {
				meta.AttributeStringValueMapping[k] = v
			}
		}
	}
	for _, tg := range targets {
		s := semTarget{
			hash:     tg.Hash,
			ruleType: meta.RuleTypeMapping[tg.RuleType],
			root:     tg.Root,
			external: tg.External,
		}
		for _, d := range tg.DirectDependencies {
			s.deps = append(s.deps, meta.TargetIDMapping[d])
		}
		for _, tag := range tg.Tags {
			s.tags = append(s.tags, meta.TagMapping[tag])
		}
		if len(tg.Attributes) > 0 {
			s.attrs = map[string]string{}
			for n, v := range tg.Attributes {
				s.attrs[meta.AttributeNameMapping[n]] = meta.AttributeStringValueMapping[v]
			}
		}
		sem[meta.TargetIDMapping[tg.ID]] = s
	}

	assert.Equal(t, map[string]semTarget{
		"//app:lib": {hash: tgbTestHash(1), ruleType: "go_library", root: true},
		"//app:src": {hash: tgbTestHash(2), ruleType: "source file", deps: []string{"//app:lib"}},
		"//app/sub:lib": {
			hash: tgbTestHash(3), ruleType: "go_library",
			tags: []string{"manual"}, attrs: map[string]string{"visibility": "//visibility:public"},
		},
	}, sem)
}

// TestTGBGraphReaderCorruptBlobs pins the storage-seam failure modes from the
// integration plan: a truncated TGB blob, a gob blob under a tgb key, and a
// zero-length blob must all surface as ErrCorruptTGB — a recompute signal,
// never a panic and never a request-failing infra error.
func TestTGBGraphReaderCorruptBlobs(t *testing.T) {
	st := storage.NewMemoryStorage()
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st, "good", tgbTestChunks()))
	resp, err := st.Get(t.Context(), storage.DownloadRequest{Key: "good"})
	require.NoError(t, err)
	blob, err := io.ReadAll(resp.ReadCloser)
	require.NoError(t, err)
	require.NoError(t, resp.ReadCloser.Close())

	put := func(key string, data []byte) {
		require.NoError(t, st.Put(t.Context(), storage.UploadRequest{Key: key, Reader: bytes.NewReader(data)}))
	}
	put("truncated", blob[:len(blob)/2])
	put("zero-length", nil)
	require.NoError(t, storage.WriteGraphStream(t.Context(), st, "gob-under-tgb-key", tgbTestChunks()))

	for _, key := range []string{"truncated", "zero-length", "gob-under-tgb-key"} {
		_, err := storage.NewTGBGraphReader(t.Context(), st, key, 1<<20)
		require.Error(t, err, key)
		assert.ErrorIs(t, err, storage.ErrCorruptTGB, key)
		assert.False(t, storage.IsNotFound(err), key)
	}
}

// TestTGBGraphReaderNotFound: a missing blob keeps the backend's not-found
// error so callers can distinguish miss from corruption.
func TestTGBGraphReaderNotFound(t *testing.T) {
	st := storage.NewMemoryStorage()
	_, err := storage.NewTGBGraphReader(t.Context(), st, "absent", 1<<20)
	require.Error(t, err)
	assert.True(t, storage.IsNotFound(err))
	assert.False(t, errors.Is(err, storage.ErrCorruptTGB))
}

// TestWriteTGBGraphRejectsBadStream: the strict encoder's contract holds
// through the storage seam — a target with no label mapping is a loud error.
func TestWriteTGBGraphRejectsBadStream(t *testing.T) {
	st := storage.NewMemoryStorage()
	chunks := []entity.GetTargetGraphResponse{
		{Targets: []entity.OptimizedTarget{{ID: 1, Hash: tgbTestHash(1), RuleType: 100}}},
		{Metadata: &entity.Metadata{
			TargetIDMapping: map[int32]string{}, // no mapping for ID 1
			RuleTypeMapping: map[int32]string{100: "go_library"},
		}},
	}
	err := storage.WriteTGBGraph(t.Context(), st, "bad", chunks)
	require.Error(t, err)
}
