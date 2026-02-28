package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	if _, err := io.Copy(&buf, req.Reader); err != nil {
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
