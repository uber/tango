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
	"context"
	"io"

	gogio "github.com/gogo/protobuf/io"
	pb "github.com/uber/tango/tangopb"
)

type GraphReader interface {
	// Read reads the next GetTargetGraphResponse message from the storage.
	Read() (*pb.GetTargetGraphResponse, error)
	// Close releases any underlying resources if supported by the implementation.
	// Implementations that don't hold resources may return nil.
	Close() error
}

// graphReader is a io.Reader that, when Read is invoked,
// streams the delimited GetTargetGraphResponse messages from Storage to the provided YARPC server stream.
// After streaming completes, subsequent Read calls return io.EOF.
type graphReaderCloser struct {
	reader gogio.ReadCloser
}

// Read reads the next message from the storage.
func (g *graphReaderCloser) Read() (*pb.GetTargetGraphResponse, error) {
	m := new(pb.GetTargetGraphResponse)
	err := g.reader.ReadMsg(m)
	if err != nil {
		return nil, err
	}
	if m.GetItem() == nil {
		return nil, io.EOF
	}
	return m, nil
}

func (g *graphReaderCloser) Close() error {
	if g.reader != nil {
		return g.reader.Close()
	}
	return nil
}

// NewGraphReader returns a GraphReader that, when read, will fetch the stored graph at key
func NewGraphReader(ctx context.Context, st Storage, key string) (GraphReader, error) {
	resp, err := st.Get(ctx, DownloadRequest{Key: key})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.ReadCloser == nil {
		return nil, nil
	}
	return &graphReaderCloser{
		reader: gogio.NewDelimitedReader(resp.ReadCloser, 512<<20), // 512MB/message limit
	}, nil
}
