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
	"io/fs"
	"strings"
	"unicode/utf8"
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

// InvalidKeyError reports a storage key that does not satisfy the portable key
// contract.
type InvalidKeyError struct {
	Key string
}

func (e *InvalidKeyError) Error() string {
	return fmt.Sprintf("invalid storage key %q", e.Key)
}

// IsInvalidKey reports whether err identifies an invalid storage key.
func IsInvalidKey(err error) bool {
	var target *InvalidKeyError
	return errors.As(err, &target)
}

// ValidateKey validates a storage key.
func ValidateKey(key string) error {
	if key == "" || key == "." || !fs.ValidPath(key) || strings.ContainsAny(key, "\\:\x00") {
		return &InvalidKeyError{Key: key}
	}
	return nil
}

// ValidateKeySegment validates one segment of a storage key.
func ValidateKeySegment(segment string) error {
	if strings.Contains(segment, "/") {
		return &InvalidKeyError{Key: segment}
	}
	return ValidateKey(segment)
}

// ValidatePrefix validates a storage list prefix. An empty prefix lists all
// keys, and a trailing slash is permitted to express a segment boundary.
func ValidatePrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if !utf8.ValidString(prefix) || strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, "\\:\x00") {
		return &InvalidKeyError{Key: prefix}
	}
	idx := strings.LastIndex(prefix, "/")
	if idx < 0 {
		return nil
	}
	if err := ValidateKey(prefix[:idx]); err != nil {
		return &InvalidKeyError{Key: prefix}
	}
	return nil
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
// Keys use portable slash-separated segments. Keys must be relative, non-empty,
// valid UTF-8, and contain no empty, ".", or ".." segments. Backslashes,
// colons, and NUL bytes are invalid. Implementations otherwise treat keys as
// opaque values.
type Storage interface {
	// Get downloads a blob from the storage. On success the returned DownloadResponse.ReadCloser
	// is non-nil and the caller owns closing it. Returns NotFoundError when the blob is not found.
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
