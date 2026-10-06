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

package storage

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/entity"
)

func TestReadJobStatus_NotFoundCases(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, st Storage)
	}{
		{name: "missing key", setup: func(*testing.T, Storage) {}},
		{name: "stale running status", setup: func(t *testing.T, st Storage) {
			status := entity.JobStatus{State: entity.JobStateRunning, LastUpdated: time.Now().Add(-time.Minute)}
			require.NoError(t, WriteJobStatus(context.Background(), st, "key", status))
		}},
		{name: "corrupt entry", setup: func(t *testing.T, st Storage) {
			require.NoError(t, st.Put(context.Background(), UploadRequest{Key: "key", Reader: bytes.NewReader([]byte("not json"))}))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := NewMemoryStorage()
			tc.setup(t, st)
			_, found, err := ReadJobStatus(context.Background(), st, "key", 10*time.Second)
			require.NoError(t, err)
			assert.False(t, found)
		})
	}
}

func TestReadWriteJobStatus(t *testing.T) {
	t.Run("round trip preserves a running status", func(t *testing.T) {
		st := NewMemoryStorage()
		want := entity.JobStatus{State: entity.JobStateRunning, LastUpdated: time.Now()}
		require.NoError(t, WriteJobStatus(context.Background(), st, "key", want))
		got, found, err := ReadJobStatus(context.Background(), st, "key", 10*time.Second)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, want.State, got.State)
		assert.True(t, want.LastUpdated.Equal(got.LastUpdated))
	})

	t.Run("a completed status is never stale", func(t *testing.T) {
		st := NewMemoryStorage()
		status := entity.JobStatus{State: entity.JobStateCompleted, LastUpdated: time.Now().Add(-time.Hour)}
		require.NoError(t, WriteJobStatus(context.Background(), st, "key", status))
		got, found, err := ReadJobStatus(context.Background(), st, "key", 10*time.Second)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, entity.JobStateCompleted, got.State)
	})

	t.Run("real storage error surfaces", func(t *testing.T) {
		injected := errors.New("storage exploded")
		_, _, err := ReadJobStatus(context.Background(), failingGetStorage{err: injected}, "key", 10*time.Second)
		require.Error(t, err)
		assert.ErrorIs(t, err, injected)
	})
}

type failingGetStorage struct {
	Storage
	err error
}

func (f failingGetStorage) Get(context.Context, DownloadRequest) (DownloadResponse, error) {
	return DownloadResponse{}, f.err
}
