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

	pb "github.com/uber/tango/tangopb"
)

type GraphReader interface {
	// Read reads the next GetTargetGraphResponse message from the storage.
	Read() (*pb.GetTargetGraphResponse, error)
	// Close releases any underlying resources if supported by the implementation.
	// Implementations that don't hold resources may return nil.
	Close() error
}

// NewGraphReader returns a GraphReader that, when read, will fetch the stored graph at key
func NewGraphReader(ctx context.Context, st Storage, key string) (GraphReader, error) {
	r, err := newReader[pb.GetTargetGraphResponse](ctx, st, key, 512<<20, func(m *pb.GetTargetGraphResponse) bool { // 512MB/message limit
		return m.GetItem() == nil
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}
