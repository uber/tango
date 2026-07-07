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

var errMarshal = errors.New("marshal failed")

type marshalErrorMessage struct{}

func (*marshalErrorMessage) Reset()         {}
func (*marshalErrorMessage) String() string { return "" }
func (*marshalErrorMessage) ProtoMessage()  {}
func (*marshalErrorMessage) Marshal() ([]byte, error) {
	return nil, errMarshal
}

type discardStorage struct{}

func (discardStorage) Get(context.Context, DownloadRequest) (DownloadResponse, error) {
	return DownloadResponse{}, nil
}

func (discardStorage) Put(context.Context, UploadRequest) error { return nil }

func (discardStorage) Exists(context.Context, string) (bool, error) { return false, nil }

func (discardStorage) List(context.Context, string) ([]string, error) { return nil, nil }

func TestWriteStreamReturnsWriterError(t *testing.T) {
	err := writeStream[marshalErrorMessage](
		context.Background(),
		discardStorage{},
		"key",
		[]*marshalErrorMessage{{}},
	)

	require.ErrorIs(t, err, errMarshal)
}

func TestMemoryStorage_List(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStorage()

	put := func(key string) {
		t.Helper()
		require.NoError(t, s.Put(ctx, UploadRequest{Key: key, Reader: bytes.NewReader([]byte("x"))}))
	}
	put("itg/repoA/2024-01-01/100_abc")
	put("itg/repoA/2024-01-02/200_def")
	put("itg/repoB/2024-01-01/300_ghi")
	put("graph/treehash123")

	t.Run("lists keys under prefix", func(t *testing.T) {
		keys, err := s.List(ctx, "itg/repoA/")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{
			"itg/repoA/2024-01-01/100_abc",
			"itg/repoA/2024-01-02/200_def",
		}, keys)
	})

	t.Run("different prefix returns different keys", func(t *testing.T) {
		keys, err := s.List(ctx, "itg/repoB/")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"itg/repoB/2024-01-01/300_ghi"}, keys)
	})

	t.Run("non-matching prefix returns empty", func(t *testing.T) {
		keys, err := s.List(ctx, "nonexistent/")
		require.NoError(t, err)
		assert.Empty(t, keys)
	})

	t.Run("empty prefix returns all keys", func(t *testing.T) {
		keys, err := s.List(ctx, "")
		require.NoError(t, err)
		assert.Len(t, keys, 4)
	})
}

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
