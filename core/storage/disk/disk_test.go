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

func TestStorage_Get_NotFound(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	s, err := New(tmpDir)
	require.NoError(t, err)

	resp, err := s.Get(ctx, storage.DownloadRequest{Key: "nonexistent.txt"})
	assert.Nil(t, resp)
	assert.Error(t, err)
	assert.True(t, storage.IsNotFound(err))
}
