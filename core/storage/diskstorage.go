package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type diskStorage struct {
	rootDir string
}

// NewDiskStorage creates a new disk-based storage implementing storage.Storage.
// The rootDir is the base directory where all blobs will be stored.
// Keys are treated as relative paths under the root directory.
func NewDiskStorage(rootDir string) (Storage, error) {
	if rootDir == "" {
		return nil, errors.New("root directory cannot be empty")
	}
	// Ensure the root directory exists
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return nil, err
	}
	return &diskStorage{
		rootDir: rootDir,
	}, nil
}

// Get downloads a blob from the storage. Return NotFoundError when the blob is not found.
func (d *diskStorage) Get(ctx context.Context, req DownloadRequest) (*DownloadResponse, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	path := filepath.Join(d.rootDir, req.Key)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &NotFoundError{Path: req.Key}
		}
		return nil, err
	}
	return &DownloadResponse{ReadCloser: file}, nil
}

// Put uploads a blob to the storage.
func (d *diskStorage) Put(ctx context.Context, req UploadRequest) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if req.Reader == nil {
		return errors.New("nil reader")
	}

	path := filepath.Join(d.rootDir, req.Key)

	// Ensure parent directories exist
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Write to a temporary file first, then rename for atomicity
	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()

	// Clean up temp file on error
	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	// Copy data to temp file
	if _, err := io.Copy(tmpFile, req.Reader); err != nil {
		tmpFile.Close()
		return err
	}

	// Sync to ensure data is written to disk
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return err
	}

	if err := tmpFile.Close(); err != nil {
		return err
	}

	// Atomically rename temp file to target path
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}

	success = true
	return nil
}
