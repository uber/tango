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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
)

func TestMemoryStorage(t *testing.T) {
	t.Run("missing key is not found", func(t *testing.T) {
		_, err := NewMemoryStorage().Get(t.Context(), "key")
		assert.True(t, storage.IsNotFound(err))
	})

	t.Run("put replaces the status", func(t *testing.T) {
		st := NewMemoryStorage()
		require.NoError(t, st.Put(t.Context(), "key", entity.JobStatus{State: entity.JobStateRunning}))
		want := entity.JobStatus{State: entity.JobStateCompleted, LastUpdated: time.Now(), ResultKey: "result"}
		require.NoError(t, st.Put(t.Context(), "key", want))
		got, err := st.Get(t.Context(), "key")
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})
}
