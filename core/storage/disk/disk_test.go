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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/storage"
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
