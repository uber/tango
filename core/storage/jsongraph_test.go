package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/entity"
)

func TestWriteAndReadJSONGraph(t *testing.T) {
	t.Parallel()

	targets := []entity.OptimizedTarget{
		{ID: 1, Hash: "aabb", RuleType: 1, DirectDependencies: []int32{2, 3}},
		{ID: 2, Hash: "ccdd", RuleType: 2, Tags: []int32{10, 11}},
		{ID: 3, Hash: "eeff", Root: true, External: true, Attributes: map[int32]int32{1: 2}},
	}
	meta := &entity.Metadata{
		TargetIDMapping: map[int32]string{1: "//a:a", 2: "//b:b", 3: "//c:c"},
		RuleTypeMapping: map[int32]string{1: "go_library", 2: "go_test"},
		TagMapping:      map[int32]string{10: "manual", 11: "no-remote"},
	}

	st := newMemStorage()

	err := WriteGraphJSON(t.Context(), st, "test-graph", targets, meta)
	require.NoError(t, err)

	resp, err := st.Get(t.Context(), DownloadRequest{Key: "test-graph"})
	require.NoError(t, err)

	// Use a large maxMessageBytes so all targets come back in one batch.
	reader, err := NewJSONGraphReader(resp.ReadCloser, 10*1024*1024)
	require.NoError(t, err)
	defer reader.Close()

	// First read: all targets in one batch.
	msg, err := reader.Read()
	require.NoError(t, err)
	require.NotNil(t, msg.GetTargets())
	assert.Len(t, msg.GetTargets().GetTargets(), 3)

	// Second read: metadata.
	msg, err = reader.Read()
	require.NoError(t, err)
	require.NotNil(t, msg.GetMetadata())
	assert.Equal(t, "//a:a", msg.GetMetadata().GetTargetIdMapping()[1])
	assert.Equal(t, "go_library", msg.GetMetadata().GetRuleTypeMapping()[1])

	// Third read: EOF.
	_, err = reader.Read()
	assert.Equal(t, io.EOF, err)
}

func TestJSONGraphReader_ChunksTargetsBySize(t *testing.T) {
	t.Parallel()

	numTargets := 50
	targets := make([]entity.OptimizedTarget, numTargets)
	for i := range targets {
		targets[i] = entity.OptimizedTarget{ID: int32(i + 1), Hash: "ab", RuleType: 1}
	}
	meta := &entity.Metadata{
		TargetIDMapping: map[int32]string{1: "//pkg:a"},
		RuleTypeMapping: map[int32]string{1: "go_library"},
	}

	st := newMemStorage()

	err := WriteGraphJSON(t.Context(), st, "test-graph", targets, meta)
	require.NoError(t, err)

	resp, err := st.Get(t.Context(), DownloadRequest{Key: "test-graph"})
	require.NoError(t, err)

	// Set maxMessageBytes to fit ~10 targets per batch.
	// Each proto target is small (~8 bytes), so use a tight limit.
	singleTargetSize := entityTargetToProto(&targets[0]).Size()
	maxBytes := singleTargetSize * 10

	reader, err := NewJSONGraphReader(resp.ReadCloser, maxBytes)
	require.NoError(t, err)
	defer reader.Close()

	var totalTargets int
	var targetChunks int
	for {
		msg, err := reader.Read()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if msg.GetTargets() != nil {
			targetChunks++
			totalTargets += len(msg.GetTargets().GetTargets())
		}
	}

	assert.Equal(t, numTargets, totalTargets)
	assert.Equal(t, 5, targetChunks, "expected 50 targets / 10 per chunk = 5 chunks")
}

func TestJSONGraphReader_EmptyTargets(t *testing.T) {
	t.Parallel()

	meta := &entity.Metadata{
		RuleTypeMapping: map[int32]string{1: "go_library"},
	}

	st := newMemStorage()

	err := WriteGraphJSON(t.Context(), st, "test-graph", nil, meta)
	require.NoError(t, err)

	resp, err := st.Get(t.Context(), DownloadRequest{Key: "test-graph"})
	require.NoError(t, err)

	reader, err := NewJSONGraphReader(resp.ReadCloser, 10*1024*1024)
	require.NoError(t, err)
	defer reader.Close()

	// First read: metadata (no targets to yield).
	msg, err := reader.Read()
	require.NoError(t, err)
	require.NotNil(t, msg.GetMetadata())
	assert.Equal(t, "go_library", msg.GetMetadata().GetRuleTypeMapping()[1])

	// Second read: EOF.
	_, err = reader.Read()
	assert.Equal(t, io.EOF, err)
}

// --- in-memory storage for tests ---

type memStorage struct {
	blobs map[string][]byte
}

func newMemStorage() *memStorage {
	return &memStorage{blobs: make(map[string][]byte)}
}

func (m *memStorage) Get(_ context.Context, req DownloadRequest) (DownloadResponse, error) {
	data, ok := m.blobs[req.Key]
	if !ok {
		return DownloadResponse{}, NewNotFoundError(req.Key)
	}
	return DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(data))}, nil
}

func (m *memStorage) Put(_ context.Context, req UploadRequest) error {
	data, err := io.ReadAll(req.Reader)
	if err != nil {
		return err
	}
	m.blobs[req.Key] = data
	return nil
}

func (m *memStorage) Exists(_ context.Context, key string) (bool, error) {
	_, ok := m.blobs[key]
	return ok, nil
}

func (m *memStorage) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range m.blobs {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// Verify that the JSON blob format is correct by decoding it directly.
func TestWriteGraphJSON_BlobFormat(t *testing.T) {
	t.Parallel()

	targets := []entity.OptimizedTarget{
		{ID: 1, Hash: "abc"},
	}
	meta := &entity.Metadata{
		TargetIDMapping: map[int32]string{1: "//a:a"},
	}

	st := newMemStorage()
	err := WriteGraphJSON(t.Context(), st, "test", targets, meta)
	require.NoError(t, err)

	var blob targetGraphBlob
	err = json.Unmarshal(st.blobs["test"], &blob)
	require.NoError(t, err)

	require.Len(t, blob.Targets, 1)
	assert.Equal(t, int32(1), blob.Targets[0].ID)
	assert.Equal(t, "abc", blob.Targets[0].Hash)
	assert.Equal(t, "//a:a", blob.Metadata.TargetIDMapping[1])
}
