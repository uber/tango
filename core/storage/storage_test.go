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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryStorage_Exists(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStorage()

	exists, err := s.Exists(ctx, "missing")
	require.NoError(t, err)
	assert.False(t, exists)

	err = s.Put(ctx, UploadRequest{Key: "present", Reader: bytes.NewReader([]byte("data"))})
	require.NoError(t, err)

	exists, err = s.Exists(ctx, "present")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestNotFoundError_Error(t *testing.T) {
	err := &NotFoundError{Path: "test/path"}
	assert.NotEmpty(t, err.Error())
}

func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "NotFoundError returns true",
			err:      &NotFoundError{Path: "test"},
			expected: true,
		},
		{
			name:     "Generic error returns false",
			err:      errors.New("generic error"),
			expected: false,
		},
		{
			name:     "Nil error returns false",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsNotFound(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
