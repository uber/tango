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
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
)

type memoryStorage struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemoryStorage creates a new in-memory storage implementing storage.Storage.
func NewMemoryStorage() Storage {
	return &memoryStorage{
		data: make(map[string][]byte),
	}
}

// Get downloads a blob from the storage. Return NotFoundError when the blob is not found.
func (m *memoryStorage) Get(ctx context.Context, req DownloadRequest) (*DownloadResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.data[req.Key]
	if !ok {
		return nil, &NotFoundError{Path: req.Key}
	}
	return &DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader(b))}, nil
}

func (m *memoryStorage) Put(ctx context.Context, req UploadRequest) error {
	if req.Reader == nil {
		return errors.New("nil reader")
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, &CtxReader{Ctx: ctx, R: req.Reader}); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[req.Key] = buf.Bytes()
	return nil
}

func (m *memoryStorage) Exists(ctx context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.data[key]
	return ok, nil
}

func (m *memoryStorage) List(ctx context.Context, dir string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys []string
	for k := range m.data {
		if strings.HasPrefix(k, dir) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}
