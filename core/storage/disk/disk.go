// Package disk provides a disk-based storage implementation.
package disk

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// NotFoundError is returned when a blob is not found.
type NotFoundError struct {
	Path string
}

func (e *NotFoundError) Error() string {
	return "blob not found at path: " + e.Path
}

// Storage is a disk-based blob storage.
type Storage struct {
	rootDir string
}

// New creates a new disk-based storage.
// The rootDir is the base directory where all blobs will be stored.
func New(rootDir string) (*Storage, error) {
	if rootDir == "" {
		return nil, errors.New("root directory cannot be empty")
	}
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return nil, err
	}
	return &Storage{rootDir: rootDir}, nil
}

// Get retrieves a blob by key. Returns NotFoundError if not found.
func (d *Storage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	path := filepath.Join(d.rootDir, key)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &NotFoundError{Path: key}
		}
		return nil, err
	}
	return file, nil
}

// Put stores a blob with the given key.
func (d *Storage) Put(ctx context.Context, key string, reader io.Reader) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if reader == nil {
		return errors.New("nil reader")
	}

	path := filepath.Join(d.rootDir, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// Write atomically via temp file
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, reader); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
