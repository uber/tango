package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDiskStorage(t *testing.T) {
	t.Run("creates storage with valid root dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		storage, err := NewDiskStorage(tmpDir)
		require.NoError(t, err)
		assert.NotNil(t, storage)
	})

	t.Run("creates root directory if not exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		newDir := filepath.Join(tmpDir, "new", "nested", "dir")
		storage, err := NewDiskStorage(newDir)
		require.NoError(t, err)
		assert.NotNil(t, storage)

		info, err := os.Stat(newDir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})

	t.Run("returns error for empty root dir", func(t *testing.T) {
		storage, err := NewDiskStorage("")
		assert.Error(t, err)
		assert.Nil(t, storage)
	})
}

func TestDiskStorage_PutAndGet(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	storage, err := NewDiskStorage(tmpDir)
	require.NoError(t, err)

	t.Run("put and get simple key", func(t *testing.T) {
		data := []byte("hello world")
		err := storage.Put(ctx, UploadRequest{
			Key:    "test.txt",
			Reader: bytes.NewReader(data),
		})
		require.NoError(t, err)

		resp, err := storage.Get(ctx, DownloadRequest{Key: "test.txt"})
		require.NoError(t, err)
		defer resp.ReadCloser.Close()

		got, err := io.ReadAll(resp.ReadCloser)
		require.NoError(t, err)
		assert.Equal(t, data, got)
	})

	t.Run("put and get nested key", func(t *testing.T) {
		data := []byte("nested content")
		key := "path/to/nested/file.bin"
		err := storage.Put(ctx, UploadRequest{
			Key:    key,
			Reader: bytes.NewReader(data),
		})
		require.NoError(t, err)

		resp, err := storage.Get(ctx, DownloadRequest{Key: key})
		require.NoError(t, err)
		defer resp.ReadCloser.Close()

		got, err := io.ReadAll(resp.ReadCloser)
		require.NoError(t, err)
		assert.Equal(t, data, got)
	})

	t.Run("put overwrites existing key", func(t *testing.T) {
		key := "overwrite.txt"

		// Write initial data
		err := storage.Put(ctx, UploadRequest{
			Key:    key,
			Reader: bytes.NewReader([]byte("initial")),
		})
		require.NoError(t, err)

		// Overwrite with new data
		newData := []byte("overwritten")
		err = storage.Put(ctx, UploadRequest{
			Key:    key,
			Reader: bytes.NewReader(newData),
		})
		require.NoError(t, err)

		// Verify new data
		resp, err := storage.Get(ctx, DownloadRequest{Key: key})
		require.NoError(t, err)
		defer resp.ReadCloser.Close()

		got, err := io.ReadAll(resp.ReadCloser)
		require.NoError(t, err)
		assert.Equal(t, newData, got)
	})

	t.Run("put with nil reader returns error", func(t *testing.T) {
		err := storage.Put(ctx, UploadRequest{
			Key:    "nil-reader.txt",
			Reader: nil,
		})
		assert.Error(t, err)
	})
}

func TestDiskStorage_Get_NotFound(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	storage, err := NewDiskStorage(tmpDir)
	require.NoError(t, err)

	resp, err := storage.Get(ctx, DownloadRequest{Key: "nonexistent.txt"})
	assert.Nil(t, resp)
	assert.Error(t, err)
	assert.True(t, IsNotFound(err))
}

func TestDiskStorage_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	storage, err := NewDiskStorage(tmpDir)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	t.Run("get with cancelled context", func(t *testing.T) {
		resp, err := storage.Get(ctx, DownloadRequest{Key: "any.txt"})
		assert.Nil(t, resp)
		assert.Error(t, err)
		assert.Equal(t, context.Canceled, err)
	})

	t.Run("put with cancelled context", func(t *testing.T) {
		err := storage.Put(ctx, UploadRequest{
			Key:    "any.txt",
			Reader: bytes.NewReader([]byte("data")),
		})
		assert.Error(t, err)
		assert.Equal(t, context.Canceled, err)
	})
}

func TestDiskStorage_LargeFile(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	storage, err := NewDiskStorage(tmpDir)
	require.NoError(t, err)

	// Create 1MB of data
	data := make([]byte, 1024*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	err = storage.Put(ctx, UploadRequest{
		Key:    "large.bin",
		Reader: bytes.NewReader(data),
	})
	require.NoError(t, err)

	resp, err := storage.Get(ctx, DownloadRequest{Key: "large.bin"})
	require.NoError(t, err)
	defer resp.ReadCloser.Close()

	got, err := io.ReadAll(resp.ReadCloser)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}
