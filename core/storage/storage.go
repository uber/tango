package storage

import (
	"context"
	"io"
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
	_, ok := err.(*NotFoundError)
	return ok
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
	// Exists checks whether a blob exists in the storage.
	Exists(ctx context.Context, key string) (bool, error)
}
