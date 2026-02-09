package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/uber/tango/core/config"
	"github.com/uber/tango/core/storage/disk"
)

// NotFoundError represents an error when a blob is not found in the storage.
type NotFoundError struct {
	Path string
}

func (e *NotFoundError) Error() string {
	return "blob not found at path: " + e.Path
}

// IsNotFound checks if an error is a NotFoundError
func IsNotFound(err error) bool {
	if _, ok := err.(*NotFoundError); ok {
		return true
	}
	if _, ok := err.(*disk.NotFoundError); ok {
		return true
	}
	return false
}

// DownloadRequest represents a request to download a blob.
type DownloadRequest struct {
	Key string
}

// DownloadResponse represents a response to a download request.
type DownloadResponse struct {
	ReadCloser io.ReadCloser
}

// UploadRequest represents a request to upload a blob.
type UploadRequest struct {
	Key    string
	Reader io.Reader
}

// Storage is an abstract interface for remote data storage.
type Storage interface {
	// Get downloads a blob from the storage. Return NotFoundError when the blob is not found.
	Get(ctx context.Context, req DownloadRequest) (*DownloadResponse, error)
	// Put uploads a blob to the storage
	Put(ctx context.Context, req UploadRequest) error
}

// NewStorage creates a Storage implementation based on the provided configuration.
func NewStorage(cfg config.StorageConfig) (Storage, error) {
	switch cfg.Type {
	case config.StorageTypeMemory, "":
		return NewMemoryStorage(), nil

	case config.StorageTypeDisk:
		if cfg.Disk == nil {
			return nil, fmt.Errorf("disk storage requires 'disk' configuration")
		}
		if cfg.Disk.RootPath == "" {
			return nil, fmt.Errorf("disk storage requires 'root_path' to be set")
		}
		d, err := disk.New(cfg.Disk.RootPath)
		if err != nil {
			return nil, err
		}
		return &diskAdapter{disk: d}, nil

	default:
		return nil, fmt.Errorf("unsupported storage type: %q", cfg.Type)
	}
}

// diskAdapter wraps disk.Storage to implement the Storage interface.
type diskAdapter struct {
	disk *disk.Storage
}

func (a *diskAdapter) Get(ctx context.Context, req DownloadRequest) (*DownloadResponse, error) {
	rc, err := a.disk.Get(ctx, req.Key)
	if err != nil {
		return nil, err
	}
	return &DownloadResponse{ReadCloser: rc}, nil
}

func (a *diskAdapter) Put(ctx context.Context, req UploadRequest) error {
	return a.disk.Put(ctx, req.Key, req.Reader)
}
