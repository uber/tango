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
	"errors"
	"fmt"
	"io"
)

// ErrNotFound is returned when a blob is not found in the storage.
var ErrNotFound = errors.New("not found")

// IsNotFound checks if err is or wraps ErrNotFound.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// NewNotFoundError wraps ErrNotFound with the key that was not found.
func NewNotFoundError(key string) error {
	return fmt.Errorf("storage get %q: %w", key, ErrNotFound)
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
//
// Keys are opaque strings; the interface has no concept of paths, directories,
// or segments. Any structure (e.g. "/"-delimited paths) is a convention of the
// caller, and implementations MUST NOT impose path semantics of their own.
type Storage interface {
	// Get downloads a blob from the storage. On success the returned DownloadResponse.ReadCloser
	// is non-nil and the caller owns closing it. Returns an error wrapping ErrNotFound when the blob is not found.
	Get(ctx context.Context, req DownloadRequest) (DownloadResponse, error)
	// Put uploads a blob to the storage
	Put(ctx context.Context, req UploadRequest) error
	// Exists checks whether a blob exists in the storage.
	Exists(ctx context.Context, key string) (bool, error)
	// List returns all keys whose name starts with the given prefix, semantically
	// equivalent to filtering the full key namespace by strings.HasPrefix(key, prefix).
	//
	// Implementations MUST treat prefix as a literal string prefix and MUST NOT
	// interpret it as a directory path. Callers control segment boundaries by
	// including a trailing "/" in their prefix: List(ctx, "foo") matches both
	// "foo/bar" and "foo-bar", while List(ctx, "foo/") matches only the former.
	//
	// An empty prefix lists every key. The returned slice is unordered and may be
	// nil when no key matches.
	List(ctx context.Context, prefix string) ([]string, error)
}
