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

	"github.com/uber/tango/entity"
)

// GraphReader streams entity.GetTargetGraphResponse values from a stored
// target graph.
type GraphReader interface {
	Read() (entity.GetTargetGraphResponse, error)
	Close() error
}

// NewGraphReader opens the stored target graph at key and returns a
// GraphReader that yields entity.GetTargetGraphResponse values.
func NewGraphReader(ctx context.Context, st Storage, key string) (GraphReader, error) {
	r, err := newReader[entity.GetTargetGraphResponse](ctx, st, key)
	if err != nil {
		return nil, err
	}
	return r, nil
}
