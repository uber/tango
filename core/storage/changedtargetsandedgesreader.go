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
	"io"

	gogio "github.com/gogo/protobuf/io"
	pb "github.com/uber/tango/tangopb"
)

// ChangedTargetsAndEdgesReader reads GetChangedTargetsAndEdgesResponse messages from storage.
type ChangedTargetsAndEdgesReader interface {
	Read() (*pb.GetChangedTargetsAndEdgesResponse, error)
	Close() error
}

type changedTargetsAndEdgesReaderCloser struct {
	reader gogio.ReadCloser
}

func (r *changedTargetsAndEdgesReaderCloser) Read() (*pb.GetChangedTargetsAndEdgesResponse, error) {
	m := new(pb.GetChangedTargetsAndEdgesResponse)
	if err := r.reader.ReadMsg(m); err != nil {
		return nil, err
	}
	if m.GetItem() == nil {
		return nil, io.EOF
	}
	return m, nil
}

func (r *changedTargetsAndEdgesReaderCloser) Close() error {
	if r.reader != nil {
		return r.reader.Close()
	}
	return nil
}

// NewChangedTargetsAndEdgesReader returns a ChangedTargetsAndEdgesReader that reads from storage at key.
func NewChangedTargetsAndEdgesReader(ctx context.Context, st Storage, key string) (ChangedTargetsAndEdgesReader, error) {
	resp, err := st.Get(ctx, DownloadRequest{Key: key})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.ReadCloser == nil {
		return nil, nil
	}
	return &changedTargetsAndEdgesReaderCloser{
		reader: gogio.NewDelimitedReader(resp.ReadCloser, 32<<20), // 32MB/message limit
	}, nil
}

// WriteChangedTargetsAndEdgesStream writes a list of GetChangedTargetsAndEdgesResponse messages to storage.
// The messages are written as length-delimited protobuf, allowing streaming reads.
func WriteChangedTargetsAndEdgesStream(ctx context.Context, st Storage, key string, responses []*pb.GetChangedTargetsAndEdgesResponse) error {
	buf := &bytes.Buffer{}
	w := gogio.NewDelimitedWriter(buf)
	for _, r := range responses {
		if err := w.WriteMsg(r); err != nil {
			return fmt.Errorf("write delimited: %w", err)
		}
	}
	return st.Put(ctx, UploadRequest{Key: key, Reader: bytes.NewReader(buf.Bytes())})
}
