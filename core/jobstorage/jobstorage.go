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

package jobstorage

import (
	"context"

	"github.com/uber/tango/entity"
)

// JobStorage stores the status of asynchronous jobs by key.
type JobStorage interface {
	// Get returns the status at key. Returns an error wrapping storage.ErrNotFound when key has no status.
	Get(ctx context.Context, key string) (entity.JobStatus, error)
	// Put writes status at key and replaces any status that is there.
	Put(ctx context.Context, key string, status entity.JobStatus) error
}
