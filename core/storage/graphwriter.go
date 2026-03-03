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
	"bytes"
	"context"
	"fmt"

	gogio "github.com/gogo/protobuf/io"
	pb "github.com/uber/tango/tangopb"
)

// WriteGraphStream writes a list of GetTargetGraphResponse messages to the storage.
// The messages are written as length-delimited protobuf, allowing streaming reads.
// Typically this includes multiple OptimizedTargets chunks followed by Metadata.
func WriteGraphStream(ctx context.Context, st Storage, key string, responses []*pb.GetTargetGraphResponse) error {
	buf := &bytes.Buffer{}
	w := gogio.NewDelimitedWriter(buf) // varint-length-delimited
	for _, r := range responses {
		if err := w.WriteMsg(r); err != nil {
			return fmt.Errorf("write delimited: %w", err)
		}
	}
	return st.Put(ctx, UploadRequest{Key: key, Reader: bytes.NewReader(buf.Bytes())})
}
