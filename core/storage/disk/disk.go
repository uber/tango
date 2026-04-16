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

// Package disk provides a disk-based storage implementation.
package disk

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/uber/tango/core/storage"
)

type diskStorage struct {
	rootDir string
}

// New creates a new disk-based storage that implements storage.Storage.
// The rootDir is the base directory where all blobs will be stored.
func New(rootDir string) (*diskStorage, error) {
	if rootDir == "" {
		return nil, errors.New("root directory cannot be empty")
	}
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return nil, err
	}
	return &diskStorage{rootDir: rootDir}, nil
}

// Get retrieves a blob by key. Returns storage.NotFoundError if not found.
func (d *diskStorage) Get(ctx context.Context, req storage.DownloadRequest) (*storage.DownloadResponse, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	path := filepath.Join(d.rootDir, req.Key)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &storage.NotFoundError{Path: req.Key}
		}
		return nil, err
	}
	return &storage.DownloadResponse{ReadCloser: file}, nil
}

// Put stores a blob with the given key.
func (d *diskStorage) Put(ctx context.Context, req storage.UploadRequest) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if req.Reader == nil {
		return errors.New("nil reader")
	}

	path := filepath.Join(d.rootDir, req.Key)
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

	if _, err := io.Copy(tmp, req.Reader); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// Exists checks whether a blob exists in the storage.
func (d *diskStorage) Exists(ctx context.Context, key string) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	_, err := os.Stat(filepath.Join(d.rootDir, key))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// List returns the relative paths of all regular files under the given directory prefix.
func (d *diskStorage) List(ctx context.Context, dir string) ([]string, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	root := filepath.Join(d.rootDir, dir)
	var keys []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(d.rootDir, path)
		if err != nil {
			return err
		}
		keys = append(keys, rel)
		return nil
	})
	return keys, err
}
