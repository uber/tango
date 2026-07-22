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

package disk

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/storage"
	"pgregory.net/rapid"
)

func TestNew(t *testing.T) {
	t.Run("creates storage with valid root dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		s, err := New(tmpDir)
		require.NoError(t, err)
		assert.NotNil(t, s)
		var _ storage.Storage = s
	})

	t.Run("creates root directory if not exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		newDir := filepath.Join(tmpDir, "new", "nested", "dir")
		s, err := New(newDir)
		require.NoError(t, err)
		assert.NotNil(t, s)

		info, err := os.Stat(newDir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})

	t.Run("returns error for empty root dir", func(t *testing.T) {
		s, err := New("")
		assert.Error(t, err)
		assert.Nil(t, s)
	})
}

func TestStorage_PutAndGet(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	s, err := New(tmpDir)
	require.NoError(t, err)

	t.Run("put and get simple key", func(t *testing.T) {
		data := []byte("hello world")
		err := s.Put(ctx, storage.UploadRequest{Key: "test.txt", Reader: bytes.NewReader(data)})
		require.NoError(t, err)

		resp, err := s.Get(ctx, storage.DownloadRequest{Key: "test.txt"})
		require.NoError(t, err)
		defer resp.ReadCloser.Close()

		got, err := io.ReadAll(resp.ReadCloser)
		require.NoError(t, err)
		assert.Equal(t, data, got)
	})

	t.Run("put and get nested key", func(t *testing.T) {
		data := []byte("nested content")
		key := "path/to/nested/file.bin"
		err := s.Put(ctx, storage.UploadRequest{Key: key, Reader: bytes.NewReader(data)})
		require.NoError(t, err)

		resp, err := s.Get(ctx, storage.DownloadRequest{Key: key})
		require.NoError(t, err)
		defer resp.ReadCloser.Close()

		got, err := io.ReadAll(resp.ReadCloser)
		require.NoError(t, err)
		assert.Equal(t, data, got)
	})
}

func TestStorage_Exists(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	s, err := New(tmpDir)
	require.NoError(t, err)

	t.Run("returns false for missing key", func(t *testing.T) {
		exists, err := s.Exists(ctx, "nonexistent.txt")
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("returns true after put", func(t *testing.T) {
		err := s.Put(ctx, storage.UploadRequest{Key: "exists.txt", Reader: bytes.NewReader([]byte("data"))})
		require.NoError(t, err)

		exists, err := s.Exists(ctx, "exists.txt")
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("returns false with cancelled context", func(t *testing.T) {
		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		exists, err := s.Exists(cancelledCtx, "any.txt")
		assert.Error(t, err)
		assert.False(t, exists)
	})
}

func TestStorage_List(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	s, err := New(tmpDir)
	require.NoError(t, err)

	put := func(key string) {
		t.Helper()
		require.NoError(t, s.Put(ctx, storage.UploadRequest{Key: key, Reader: bytes.NewReader([]byte("x"))}))
	}
	put("itg/repoA/2024-01-01/100_abc")
	put("itg/repoA/2024-01-02/200_def")
	put("itg/repoB/2024-01-01/300_ghi")
	put("graph/treehash123")

	t.Run("lists files under subdirectory", func(t *testing.T) {
		keys, err := s.List(ctx, "itg/repoA")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{
			"itg/repoA/2024-01-01/100_abc",
			"itg/repoA/2024-01-02/200_def",
		}, keys)
	})

	t.Run("different subdirectory returns different keys", func(t *testing.T) {
		keys, err := s.List(ctx, "itg/repoB")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"itg/repoB/2024-01-01/300_ghi"}, keys)
	})

	t.Run("non-existent directory returns empty", func(t *testing.T) {
		keys, err := s.List(ctx, "nonexistent")
		require.NoError(t, err)
		assert.Empty(t, keys)
	})

	t.Run("empty dir lists all files", func(t *testing.T) {
		keys, err := s.List(ctx, "")
		require.NoError(t, err)
		assert.Len(t, keys, 4)
	})

	t.Run("cancelled context returns error", func(t *testing.T) {
		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := s.List(cancelledCtx, "itg")
		assert.Error(t, err)
	})

	t.Run("partial-segment prefix matches sibling keys (literal prefix)", func(t *testing.T) {
		// Both "itg/repoA..." and "itg/repoB..." start with "itg/repo" — the
		// literal-prefix contract returns both, even though they are different
		// "directories" in a filesystem sense.
		keys, err := s.List(ctx, "itg/repo")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{
			"itg/repoA/2024-01-01/100_abc",
			"itg/repoA/2024-01-02/200_def",
			"itg/repoB/2024-01-01/300_ghi",
		}, keys)
	})

	t.Run("trailing slash delimits segment", func(t *testing.T) {
		// Same data, but the trailing "/" enforces a segment boundary so only
		// repoA's keys match.
		keys, err := s.List(ctx, "itg/repoA/")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{
			"itg/repoA/2024-01-01/100_abc",
			"itg/repoA/2024-01-02/200_def",
		}, keys)
	})

	t.Run("top-level partial prefix without slash", func(t *testing.T) {
		// "g" matches "graph/treehash123" only — proves the walk doesn't require
		// the prefix to name a real directory.
		keys, err := s.List(ctx, "g")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"graph/treehash123"}, keys)
	})
}

func TestStorage_Get_NotFound(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	s, err := New(tmpDir)
	require.NoError(t, err)

	resp, err := s.Get(ctx, storage.DownloadRequest{Key: "nonexistent.txt"})
	assert.Nil(t, resp.ReadCloser)
	assert.Error(t, err)
	assert.True(t, storage.IsNotFound(err))
}

func TestStorage_operationSequence(t *testing.T) {
	tests := []struct {
		name string
		new  func(*testing.T) storage.Storage
	}{
		{
			name: "Memory",
			new: func(*testing.T) storage.Storage {
				return storage.NewMemoryStorage()
			},
		},
		{
			name: "Disk",
			new: func(t *testing.T) storage.Storage {
				s, err := New(t.TempDir())
				require.NoError(t, err)
				return s
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rapid.Check(t, func(rt *rapid.T) {
				operations := rapid.SliceOfN(storageOperationGenerator(), 0, 50).Draw(rt, "operations")
				anchor := rapid.StringMatching(`[a-z]{1,8}`).Draw(rt, "anchor")
				operations = append(operations,
					storageOperation{kind: "Put", key: anchor, value: "parent"},
					storageOperation{kind: "Put", key: anchor + "/child", value: "child"},
				)
				checkStorageOperations(rt, t.Context(), tt.new(t), operations)
			})
		})
	}
}

func TestStorage_opaqueKeysDoNotAlias(t *testing.T) {
	tests := []struct {
		name  string
		first string
		alias string
	}{
		{name: "ParentTraversal", first: "blob", alias: "segment/../blob"},
		{name: "CurrentDirectory", first: "segment/blob", alias: "segment/./blob"},
		{name: "RepeatedSeparator", first: "segment/blob", alias: "segment//blob"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := New(t.TempDir())
			require.NoError(t, err)

			require.NoError(t, s.Put(t.Context(), storage.UploadRequest{
				Key:    tt.first,
				Reader: strings.NewReader("first"),
			}))
			require.NoError(t, s.Put(t.Context(), storage.UploadRequest{
				Key:    tt.alias,
				Reader: strings.NewReader("alias"),
			}))

			assertStoredValue(t, s, tt.first, "first")
			assertStoredValue(t, s, tt.alias, "alias")
			keys, err := s.List(t.Context(), "")
			require.NoError(t, err)
			assert.ElementsMatch(t, []string{tt.first, tt.alias}, keys)
		})
	}
}

func TestStorage_traversalCannotEscapeRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	canaryPath := filepath.Join(parent, "canary")

	rapid.Check(t, func(rt *rapid.T) {
		require.NoError(rt, os.RemoveAll(root))
		s, err := New(root)
		require.NoError(rt, err)
		canary := rapid.StringMatching(`[a-z]{1,16}`).Draw(rt, "canary")
		value := rapid.StringMatching(`[A-Z]{1,16}`).Draw(rt, "value")
		traversal := rapid.SampledFrom([]string{
			"../canary",
			"./../canary",
			"segment/../../canary",
			"segment/../..//canary",
		}).Draw(rt, "traversal")
		require.NoError(rt, os.WriteFile(canaryPath, []byte(canary), 0o600))

		exists, err := s.Exists(t.Context(), traversal)
		require.NoError(rt, err)
		beforePut, beforePutErr := s.Get(t.Context(), storage.DownloadRequest{Key: traversal})
		if beforePut.ReadCloser != nil {
			assert.NoError(rt, beforePut.ReadCloser.Close())
		}

		require.NoError(rt, s.Put(t.Context(), storage.UploadRequest{
			Key:    traversal,
			Reader: strings.NewReader(value),
		}))

		gotCanary, err := os.ReadFile(canaryPath)
		require.NoError(rt, err)
		require.Equal(rt, canary, string(gotCanary))
		assert.False(rt, exists)
		assert.True(rt, storage.IsNotFound(beforePutErr), "outside-root canary was visible through key %q", traversal)
		response, err := s.Get(t.Context(), storage.DownloadRequest{Key: traversal})
		require.NoError(rt, err)
		require.NotNil(rt, response.ReadCloser)
		defer func() {
			assert.NoError(rt, response.ReadCloser.Close())
		}()
		got, err := io.ReadAll(response.ReadCloser)
		require.NoError(rt, err)
		assert.Equal(rt, value, string(got))
	})
}

type storageOperation struct {
	kind   string
	key    string
	value  string
	prefix string
}

func storageOperationGenerator() *rapid.Generator[storageOperation] {
	return rapid.Custom(func(t *rapid.T) storageOperation {
		key := rapid.StringMatching(`[a-z0-9_-]{1,8}(/[a-z0-9_-]{1,8}){0,2}`).Draw(t, "key")
		prefixEnd := rapid.IntRange(0, len(key)).Draw(t, "prefixEnd")
		return storageOperation{
			kind:   rapid.SampledFrom([]string{"Put", "Get", "Exists", "List"}).Draw(t, "kind"),
			key:    key,
			value:  rapid.StringMatching(`[A-Za-z0-9]{0,24}`).Draw(t, "value"),
			prefix: key[:prefixEnd],
		}
	})
}

func checkStorageOperations(t *rapid.T, ctx context.Context, s storage.Storage, operations []storageOperation) {
	t.Helper()
	model := make(map[string]string)
	for i, operation := range operations {
		switch operation.kind {
		case "Put":
			err := s.Put(ctx, storage.UploadRequest{
				Key:    operation.key,
				Reader: strings.NewReader(operation.value),
			})
			require.NoError(t, err, "operation %d: %#v", i, operation)
			model[operation.key] = operation.value
		case "Get":
			assertGetMatchesModel(t, ctx, s, model, operation.key, i)
		case "Exists":
			exists, err := s.Exists(ctx, operation.key)
			require.NoError(t, err, "operation %d: %#v", i, operation)
			_, want := model[operation.key]
			assert.Equal(t, want, exists, "operation %d: %#v", i, operation)
		case "List":
			assertListMatchesModel(t, ctx, s, model, operation.prefix, i)
		default:
			t.Fatalf("operation %d has unknown kind %q", i, operation.kind)
		}
	}

	for key := range model {
		assertGetMatchesModel(t, ctx, s, model, key, len(operations))
		exists, err := s.Exists(ctx, key)
		require.NoError(t, err)
		assert.True(t, exists, "key %q", key)
	}
	assertGetMatchesModel(t, ctx, s, model, "portable-missing-key", len(operations))
	assertListMatchesModel(t, ctx, s, model, "", len(operations))
}

func assertGetMatchesModel(t *rapid.T, ctx context.Context, s storage.Storage, model map[string]string, key string, operation int) {
	t.Helper()
	response, err := s.Get(ctx, storage.DownloadRequest{Key: key})
	want, exists := model[key]
	if !exists {
		assert.Error(t, err, "operation %d: key %q", operation, key)
		assert.True(t, storage.IsNotFound(err), "operation %d: key %q: %v", operation, key, err)
		assert.Nil(t, response.ReadCloser)
		return
	}
	require.NoError(t, err, "operation %d: key %q", operation, key)
	require.NotNil(t, response.ReadCloser)
	defer func() {
		assert.NoError(t, response.ReadCloser.Close())
	}()
	got, err := io.ReadAll(response.ReadCloser)
	require.NoError(t, err)
	assert.Equal(t, want, string(got), "operation %d: key %q", operation, key)
}

func assertListMatchesModel(t *rapid.T, ctx context.Context, s storage.Storage, model map[string]string, prefix string, operation int) {
	t.Helper()
	var want []string
	for key := range model {
		if strings.HasPrefix(key, prefix) {
			want = append(want, key)
		}
	}
	got, err := s.List(ctx, prefix)
	require.NoError(t, err, "operation %d: prefix %q", operation, prefix)
	assert.ElementsMatch(t, want, got, "operation %d: prefix %q", operation, prefix)
}

func assertStoredValue(t *testing.T, s storage.Storage, key, want string) {
	t.Helper()
	response, err := s.Get(t.Context(), storage.DownloadRequest{Key: key})
	require.NoError(t, err)
	require.NotNil(t, response.ReadCloser)
	defer func() {
		assert.NoError(t, response.ReadCloser.Close())
	}()
	got, err := io.ReadAll(response.ReadCloser)
	require.NoError(t, err)
	assert.Equal(t, want, string(got))
}
