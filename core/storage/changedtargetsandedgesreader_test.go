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
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/uber/tango/tangopb"
)

func TestWriteAndReadChangedTargetsAndEdgesStream_RoundTrip(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStorage()

	responses := []*pb.GetChangedTargetsAndEdgesResponse{
		{
			Item: &pb.GetChangedTargetsAndEdgesResponse_ChangedTargetsAndEdges{
				ChangedTargetsAndEdges: &pb.ChangedTargetsAndEdges{
					ChangedTargets: []*pb.ChangedTarget{
						{ChangeType: pb.CHANGE_TYPE_DIRECT},
					},
				},
			},
		},
		{
			Item: &pb.GetChangedTargetsAndEdgesResponse_Metadata{
				Metadata: &pb.Metadata{
					TargetIdMapping: map[int32]string{1: "//app:A"},
				},
			},
		},
	}

	err := WriteChangedTargetsAndEdgesStream(ctx, st, "test-key", responses)
	require.NoError(t, err)

	reader, err := NewChangedTargetsAndEdgesReader(ctx, st, "test-key")
	require.NoError(t, err)
	require.NotNil(t, reader)
	defer reader.Close()

	for _, want := range responses {
		got, err := reader.Read()
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, want, got)
	}

	_, err = reader.Read()
	assert.Equal(t, io.EOF, err)
}

func TestWriteAndReadChangedTargetsAndEdgesStream_Empty(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStorage()

	err := WriteChangedTargetsAndEdgesStream(ctx, st, "empty-key", nil)
	require.NoError(t, err)

	reader, err := NewChangedTargetsAndEdgesReader(ctx, st, "empty-key")
	require.NoError(t, err)
	require.NotNil(t, reader)
	defer reader.Close()

	_, err = reader.Read()
	assert.Equal(t, io.EOF, err)
}

func TestNewChangedTargetsAndEdgesReader_NotFound(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStorage()

	_, err := NewChangedTargetsAndEdgesReader(ctx, st, "missing-key")
	require.Error(t, err)
	assert.True(t, IsNotFound(err))
}

func TestNewChangedTargetsAndEdgesReader_StorageError(t *testing.T) {
	ctx := context.Background()
	// errStorage always returns a non-NotFound error from Get.
	st := &errStorage{err: errors.New("infra failure")}

	_, err := NewChangedTargetsAndEdgesReader(ctx, st, "any-key")
	require.Error(t, err)
	assert.False(t, IsNotFound(err))
}

func TestNewChangedTargetsAndEdgesReader_NilResponse(t *testing.T) {
	ctx := context.Background()
	st := &nilResponseStorage{}

	reader, err := NewChangedTargetsAndEdgesReader(ctx, st, "any-key")
	require.NoError(t, err)
	assert.Nil(t, reader)
}

func TestChangedTargetsAndEdgesReader_Close_NilReader(t *testing.T) {
	r := &changedTargetsAndEdgesReaderCloser{reader: nil}
	assert.NoError(t, r.Close())
}

// errStorage is a Storage stub that returns a fixed error from Get.
type errStorage struct {
	err error
}

func (s *errStorage) Get(_ context.Context, _ DownloadRequest) (*DownloadResponse, error) {
	return nil, s.err
}
func (s *errStorage) Put(_ context.Context, _ UploadRequest) error { return nil }
func (s *errStorage) Exists(_ context.Context, _ string) (bool, error) {
	return false, s.err
}
func (s *errStorage) List(_ context.Context, _ string) ([]string, error) { return nil, s.err }

// nilResponseStorage is a Storage stub that returns a nil DownloadResponse from Get.
type nilResponseStorage struct{}

func (s *nilResponseStorage) Get(_ context.Context, _ DownloadRequest) (*DownloadResponse, error) {
	return nil, nil
}
func (s *nilResponseStorage) Put(_ context.Context, _ UploadRequest) error { return nil }
func (s *nilResponseStorage) Exists(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (s *nilResponseStorage) List(_ context.Context, _ string) ([]string, error) { return nil, nil }
