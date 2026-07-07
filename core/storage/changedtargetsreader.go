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
	"fmt"

	gogio "github.com/gogo/protobuf/io"
	pb "github.com/uber/tango/tangopb"
)

// ChangedTargetsReader reads GetChangedTargetsResponse messages from storage.
type ChangedTargetsReader interface {
	Read() (*pb.GetChangedTargetsResponse, error)
	Close() error
}

// NewChangedTargetsReader returns a ChangedTargetsReader that reads from storage at key.
func NewChangedTargetsReader(ctx context.Context, st Storage, key string) (ChangedTargetsReader, error) {
	r, err := newReader[pb.GetChangedTargetsResponse](ctx, st, key, 32<<20, func(m *pb.GetChangedTargetsResponse) bool {
		return m.GetItem() == nil
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// WriteChangedTargetsStream writes a list of GetChangedTargetsResponse messages to storage.
// The messages are written as length-delimited protobuf, allowing streaming reads.
func WriteChangedTargetsStream(ctx context.Context, st Storage, key string, responses []*pb.GetChangedTargetsResponse) error {
	buf := &bytes.Buffer{}
	w := gogio.NewDelimitedWriter(buf)
	for _, r := range responses {
		if err := w.WriteMsg(r); err != nil {
			return fmt.Errorf("write delimited: %w", err)
		}
	}
	return st.Put(ctx, UploadRequest{Key: key, Reader: bytes.NewReader(buf.Bytes())})
}
