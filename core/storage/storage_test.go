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
