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
	"fmt"
	"io"

	gogio "github.com/gogo/protobuf/io"
	pb "github.com/uber/tango/tangopb"
)

// writeStream marshals msgs as length-delimited protobuf and streams them to
// storage under key. It uses an io.Pipe so the serialized payload is never
// buffered in full a second time: a writer goroutine encodes into the pipe
// while Put consumes from it.
//
// The writer goroutine checks ctx before each message so a cancellation
// unwinds the encode loop promptly instead of waiting for Put to notice and
// stop reading; the context error is propagated to the reader. If Put returns
// before draining the pipe, the reader is closed to unblock the writer. The
// goroutine is joined before returning, and its error is returned when Put
// succeeds.
func writeStream[T any, PT protoMessage[T]](ctx context.Context, st Storage, key string, msgs []PT) error {
	pr, pw := io.Pipe()
	writerErr := make(chan error, 1)
	go func() {
		w := gogio.NewDelimitedWriter(pw) // varint-length-delimited
		var err error
		for _, m := range msgs {
			if err = ctx.Err(); err != nil {
				break
			}
			if err = w.WriteMsg(m); err != nil {
				err = fmt.Errorf("write delimited: %w", err)
				break
			}
		}
		pw.CloseWithError(err)
		writerErr <- err
	}()
	putErr := st.Put(ctx, UploadRequest{Key: key, Reader: pr})
	// Unblock the writer goroutine if Put stopped reading early.
	pr.CloseWithError(putErr)
	writeErr := <-writerErr
	if putErr != nil {
		return putErr
	}
	return writeErr
}

// WriteGraphStream writes a list of GetTargetGraphResponse messages to the storage.
// The messages are written as length-delimited protobuf, allowing streaming reads.
// Typically this includes multiple OptimizedTargets chunks followed by Metadata.
func WriteGraphStream(ctx context.Context, st Storage, key string, responses []*pb.GetTargetGraphResponse) error {
	return writeStream[pb.GetTargetGraphResponse](ctx, st, key, responses)
}
