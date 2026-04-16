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
	// List returns the keys of all blobs under the given directory prefix.
	List(ctx context.Context, dir string) ([]string, error)
}
