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

package controller

import (
	"context"
	"io"

	"github.com/uber/tango/core/common"
	"github.com/uber/tango/core/storage"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/zap"
)

// cacheReader is the minimal contract satisfied by the streaming cache
// readers used for cached comparison results.
type cacheReader[T any] interface {
	Read() (T, error)
	Close() error
}

// readTreehash fetches the treehash stored at GetTreehashCachePath for the
// given build description. Returns an empty string on any error or cache miss
// so callers can treat it as an optional optimistic lookup.
func readTreehash(ctx context.Context, st storage.Storage, buildDescription *pb.BuildDescription) string {
	resp, err := st.Get(ctx, storage.DownloadRequest{Key: common.GetTreehashCachePath(buildDescription)})
	if err != nil || resp == nil || resp.ReadCloser == nil {
		return ""
	}
	defer resp.ReadCloser.Close()
	b, err := io.ReadAll(resp.ReadCloser)
	if err != nil {
		return ""
	}
	return string(b)
}

// readTreehashPair fetches the treehashes for both revisions and reports
// whether both are available. ok=false means the cache lookup should be
// skipped (no key can be computed).
func readTreehashPair(ctx context.Context, st storage.Storage, first, second *pb.BuildDescription) (h1, h2 string, ok bool) {
	h1 = readTreehash(ctx, st, first)
	h2 = readTreehash(ctx, st, second)
	return h1, h2, h1 != "" && h2 != ""
}

// loadCachedResponses opens a cache reader via openFn and drains it into a
// slice. Returns (items, true) on a clean hit (including a zero-length hit);
// returns (nil, false) when the cache cannot be opened or the stored blob is
// corrupt. Infrastructure-level errors and corruption are logged as warnings
// so callers can silently fall through to recompute.
func loadCachedResponses[T any](
	ctx context.Context,
	logger *zap.Logger,
	rpcName string,
	openFn func(ctx context.Context) (cacheReader[T], error),
) ([]T, bool) {
	reader, err := openFn(ctx)
	if err != nil {
		if !storage.IsNotFound(err) {
			logger.Warn(rpcName+": Failed to read from cache, proceeding to compute", zap.Error(err))
		}
		return nil, false
	}
	if reader == nil {
		return nil, false
	}
	defer reader.Close()
	var out []T
	for {
		m, readErr := reader.Read()
		if readErr == io.EOF {
			return out, true
		}
		if readErr != nil {
			// Blob is corrupt (likely an incomplete write). Log and fall through to recompute.
			logger.Warn(rpcName+": Cached result is incomplete, recomputing", zap.Error(readErr))
			return nil, false
		}
		out = append(out, m)
	}
}
