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
	"io"

	gogio "github.com/gogo/protobuf/io"
	gogoproto "github.com/gogo/protobuf/proto"
)

// protoMessage is the constraint satisfied by generated gogoproto message
// pointer types used with reader.
type protoMessage[T any] interface {
	*T
	gogoproto.Message
}

// reader streams length-delimited protobuf messages of type T from storage,
// treating a message for which isEmpty returns true as the stream terminator.
type reader[T any, PT protoMessage[T]] struct {
	rc      gogio.ReadCloser
	isEmpty func(PT) bool
}

// Read reads the next message from the storage.
func (r *reader[T, PT]) Read() (PT, error) {
	m := PT(new(T))
	if err := r.rc.ReadMsg(m); err != nil {
		return nil, err
	}
	if r.isEmpty(m) {
		return nil, io.EOF
	}
	return m, nil
}

// Close releases any underlying resources.
func (r *reader[T, PT]) Close() error {
	if r.rc != nil {
		return r.rc.Close()
	}
	return nil
}

// newReader opens the blob at key and returns a reader that decodes
// length-delimited T messages from it, up to maxMessageSize bytes/message.
func newReader[T any, PT protoMessage[T]](ctx context.Context, st Storage, key string, maxMessageSize int, isEmpty func(PT) bool) (*reader[T, PT], error) {
	resp, err := st.Get(ctx, DownloadRequest{Key: key})
	if err != nil {
		return nil, err
	}
	return &reader[T, PT]{
		rc:      gogio.NewDelimitedReader(resp.ReadCloser, maxMessageSize),
		isEmpty: isEmpty,
	}, nil
}
