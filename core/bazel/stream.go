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

package bazel

import (
	"bufio"
	"context"
	"io"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"google.golang.org/protobuf/encoding/protodelim"
)

func streamOutput(ctx context.Context, src io.Reader, dst io.Writer) error {
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(dst, src)
		done <- err
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func streamAndParseTargets(ctx context.Context, src io.Reader, dst io.Writer) (*buildpb.QueryResult, error) {
	type result struct {
		queryResult *buildpb.QueryResult
		err         error
	}
	done := make(chan result, 1)

	go func() {
		queryResult, err := getQueryResult(ctx, src, dst)
		done <- result{queryResult: queryResult, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-done:
		return res.queryResult, res.err
	}
}

// cancelCheckInterval is how often we poll ctx.Err() inside per-target hot loops.
// Picked to keep overhead negligible while still surfacing cancellation in <100ms
// for typical target rates.
const cancelCheckInterval = 1024

// getQueryResult reads a QueryResult containing targets from the stream and returns it.
func getQueryResult(ctx context.Context, src io.Reader, dst io.Writer) (*buildpb.QueryResult, error) {
	result := &buildpb.QueryResult{
		Target: make([]*buildpb.Target, 0),
	}
	tr := io.TeeReader(src, dst)
	br := bufio.NewReader(tr)
	unmarshalOpts := protodelim.UnmarshalOptions{
		MaxSize: 64 * 1024 * 1024, // 64MB limit
	}
	var parseErr error
	for i := 0; ; i++ {
		if i%cancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return result, err
			}
		}
		var target buildpb.Target
		err := unmarshalOpts.UnmarshalFrom(br, &target)
		if err == io.EOF {
			break
		}
		if err != nil {
			parseErr = err
			// Continue reading - critical to prevent Bazel from blocking on write
			continue
		}
		result.Target = append(result.Target, &target)
	}

	return result, parseErr
}
