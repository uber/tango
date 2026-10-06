// Copyright (c) 2026 Uber Technologies, Inc.
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

package jobstorage

import (
	"context"
	"sync"

	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
)

type memoryStorage struct {
	mu   sync.RWMutex
	data map[string]entity.JobStatus
}

// NewMemoryStorage creates a JobStorage that keeps job status in process memory.
func NewMemoryStorage() JobStorage {
	return &memoryStorage{data: make(map[string]entity.JobStatus)}
}

func (m *memoryStorage) Get(ctx context.Context, key string) (entity.JobStatus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	status, ok := m.data[key]
	if !ok {
		return entity.JobStatus{}, storage.NewNotFoundError(key)
	}
	return status, nil
}

func (m *memoryStorage) Put(ctx context.Context, key string, status entity.JobStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = status
	return nil
}
