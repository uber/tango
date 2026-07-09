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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/uber/tango/core/storage"
)

type diskStorage struct {
	rootDir string
}

var _ storage.Storage = (*diskStorage)(nil)

// New creates a new disk-based storage that implements storage.Storage.
// The rootDir is the base directory where all blobs will be stored.
func New(rootDir string) (storage.Storage, error) {
	if rootDir == "" {
		return nil, errors.New("root directory cannot be empty")
	}
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return nil, err
	}
	return &diskStorage{rootDir: rootDir}, nil
}

// Get retrieves a blob by key. Returns storage.NotFoundError if not found.
func (d *diskStorage) Get(ctx context.Context, req storage.DownloadRequest) (storage.DownloadResponse, error) {
	if ctx.Err() != nil {
		return storage.DownloadResponse{}, ctx.Err()
	}
	if err := storage.ValidateKey(req.Key); err != nil {
		return storage.DownloadResponse{}, err
	}
	root, err := os.OpenRoot(d.rootDir)
	if err != nil {
		return storage.DownloadResponse{}, err
	}
	defer root.Close()

	file, err := root.Open(req.Key)
	if err != nil {
		if os.IsNotExist(err) {
			return storage.DownloadResponse{}, &storage.NotFoundError{Path: req.Key}
		}
		return storage.DownloadResponse{}, err
	}
	return storage.DownloadResponse{ReadCloser: file}, nil
}

// Put stores a blob with the given key.
func (d *diskStorage) Put(ctx context.Context, req storage.UploadRequest) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if req.Reader == nil {
		return errors.New("nil reader")
	}
	if err := storage.ValidateKey(req.Key); err != nil {
		return err
	}
	root, err := os.OpenRoot(d.rootDir)
	if err != nil {
		return err
	}
	defer root.Close()

	dir := path.Dir(req.Key)
	if dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	tmp, tmpPath, err := createTemp(root, dir)
	if err != nil {
		return err
	}
	defer root.Remove(tmpPath)

	if _, err := io.Copy(tmp, &storage.CtxReader{Ctx: ctx, R: req.Reader}); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return root.Rename(tmpPath, req.Key)
}

func createTemp(root *os.Root, dir string) (*os.File, string, error) {
	for range 10 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", err
		}
		name := ".tmp-" + hex.EncodeToString(random[:])
		if dir != "." {
			name = path.Join(dir, name)
		}
		file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, name, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("create unique temporary file")
}

// Exists checks whether a blob exists in the storage.
func (d *diskStorage) Exists(ctx context.Context, key string) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}
	root, err := os.OpenRoot(d.rootDir)
	if err != nil {
		return false, err
	}
	defer root.Close()

	_, err = root.Stat(key)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// List returns all keys whose name starts with the given prefix.
//
// To honor the literal-prefix contract without walking the entire rootDir, this
// walks the longest path-prefix of `prefix` ending in "/" (or rootDir if none)
// and filters entries by strings.HasPrefix on the full key.
func (d *diskStorage) List(ctx context.Context, prefix string) ([]string, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := storage.ValidatePrefix(prefix); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(d.rootDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	walkSubdir := "."
	if idx := strings.LastIndex(prefix, "/"); idx >= 0 {
		walkSubdir = prefix[:idx]
	}
	var keys []string
	err = fs.WalkDir(root.FS(), walkSubdir, func(key string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		keys = append(keys, key)
		return nil
	})
	return keys, err
}
