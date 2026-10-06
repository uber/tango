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
	"encoding/json"
	"io"
	"time"

	"github.com/uber/tango/entity"
)

// ReadJobStatus reads the job status at key. A missing, corrupt, or stale entry is not found.
func ReadJobStatus(ctx context.Context, st Storage, key string, heartbeatInterval time.Duration) (entity.JobStatus, bool, error) {
	resp, err := st.Get(ctx, DownloadRequest{Key: key})
	if err != nil {
		if IsNotFound(err) {
			return entity.JobStatus{}, false, nil
		}
		return entity.JobStatus{}, false, err
	}
	defer func() { _ = resp.ReadCloser.Close() }()
	b, err := io.ReadAll(resp.ReadCloser)
	if err != nil {
		return entity.JobStatus{}, false, err
	}
	var status entity.JobStatus
	if err := json.Unmarshal(b, &status); err != nil {
		return entity.JobStatus{}, false, nil
	}
	if isJobStale(status, heartbeatInterval) {
		return entity.JobStatus{}, false, nil
	}
	return status, true, nil
}

func isJobStale(status entity.JobStatus, heartbeatInterval time.Duration) bool {
	return status.State == entity.JobStateRunning && time.Since(status.LastUpdated) > entity.JobStaleAfter(heartbeatInterval)
}

// WriteJobStatus writes status as JSON at key.
func WriteJobStatus(ctx context.Context, st Storage, key string, status entity.JobStatus) error {
	b, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return st.Put(ctx, UploadRequest{Key: key, Reader: bytes.NewReader(b)})
}
