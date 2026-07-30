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

package cache

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uber/tango/core/itg/graph"
	"github.com/uber/tango/core/storage"
)

func TestStorageCacheRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		remote string
	}{
		{
			name:   "URL remote with double slash",
			remote: "https://github.com/uber/tango",
		},
		{
			name:   "plain path remote",
			remote: "uber/tango",
		},
		{
			name:   "SSH remote",
			remote: "git@github.com:uber/tango.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := storage.NewMemoryStorage()
			c := NewStorageCache(s)
			ctx := context.Background()

			key := Key{
				Remote:               tt.remote,
				BaseCommitTimeSecond: 1700000000,
				BaseSha:              "abc123",
			}

			og := &graph.OptimizedGraph{
				OptimizedTargets: map[int]*graph.OptimizedTarget{
					1: {ID: 1, Hash: []byte("hash1")},
				},
			}

			// Write entry via Put.
			err := c.Put(ctx, og, key)
			require.NoError(t, err)

			// FloorKey must discover the entry we just wrote.
			found, err := c.FloorKey(ctx, tt.remote, key.BaseCommitTimeSecond)
			require.NoError(t, err)
			assert.Equal(t, key.BaseCommitTimeSecond, found.BaseCommitTimeSecond)
			assert.Equal(t, key.BaseSha, found.BaseSha)
			assert.Equal(t, tt.remote, found.Remote)

			// Get must return the stored graph.
			got, err := c.Get(ctx, key)
			require.NoError(t, err)
			require.Len(t, got.OptimizedTargets, 1)
			assert.Equal(t, 1, got.OptimizedTargets[1].ID)
		})
	}
}

func TestFloorKeyEmpty(t *testing.T) {
	s := storage.NewMemoryStorage()
	c := NewStorageCache(s)
	ctx := context.Background()

	found, err := c.FloorKey(ctx, "https://github.com/uber/tango", 1700000000)
	require.NoError(t, err)
	assert.Equal(t, EmptyKey, found)
}
