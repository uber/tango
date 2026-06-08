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
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uber/tango/core/storage"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/zap"
)

// latencyStorage is a storage.Storage stub that sleeps for a fixed duration on
// every Get call to simulate real-world network/disk latency in benchmarks.
type latencyStorage struct {
	latency time.Duration
}

func (s *latencyStorage) Get(_ context.Context, _ storage.DownloadRequest) (*storage.DownloadResponse, error) {
	time.Sleep(s.latency)
	return &storage.DownloadResponse{ReadCloser: io.NopCloser(strings.NewReader("treehash"))}, nil
}
func (s *latencyStorage) Put(_ context.Context, _ storage.UploadRequest) error { return nil }
func (s *latencyStorage) Exists(_ context.Context, _ string) (bool, error)     { return true, nil }

// BenchmarkTreehashReads compares the old sequential approach against the
// current parallel approach at several simulated storage latencies.
func BenchmarkTreehashReads(b *testing.B) {
	desc := &pb.BuildDescription{Remote: "repo", BaseSha: "abc123"}

	for _, latency := range []time.Duration{1 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond} {
		st := &latencyStorage{latency: latency}

		b.Run(fmt.Sprintf("sequential/latency=%s", latency), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = readTreehash(context.Background(), st, desc)
				_ = readTreehash(context.Background(), st, desc)
			}
		})

		b.Run(fmt.Sprintf("parallel/latency=%s", latency), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				var h1, h2 string
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); h1 = readTreehash(context.Background(), st, desc) }()
				go func() { defer wg.Done(); h2 = readTreehash(context.Background(), st, desc) }()
				wg.Wait()
				_, _ = h1, h2
			}
		})
	}
}

// BenchmarkComputeDistances measures BFS over a linear chain where every target
// is changed: T0 is DIRECT, T1..TN-1 are INDIRECT dependants.
// This exercises the worst-case traversal depth.
func BenchmarkComputeDistances(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			meta, targetsByName := buildChainGraph(n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				changedByName := make(map[string]*pb.ChangedTarget, n)
				changedByName["//app:T0"] = &pb.ChangedTarget{ChangeType: pb.CHANGE_TYPE_DIRECT}
				for j := 1; j < n; j++ {
					changedByName[fmt.Sprintf("//app:T%d", j)] = &pb.ChangedTarget{ChangeType: pb.CHANGE_TYPE_INDIRECT}
				}
				computeDistances(zap.NewNop(), changedByName, targetsByName, meta, -1)
			}
		})
	}
}

// BenchmarkCompareTargetGraphs measures the full diff pipeline — index building,
// change detection, classification, and distance computation — across two graphs
// where every target has a changed hash.
func BenchmarkCompareTargetGraphs(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			first := buildBenchGraph(n, "v1")
			second := buildBenchGraph(n, "v2")
			c := newTestController(zap.NewNop())
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = c.compareTargetGraphs(zap.NewNop(), first, second, -1, false)
			}
		})
	}
}

// buildChainGraph returns graph data for computeDistances benchmarks:
// a chain T0 <- T1 <- ... <- TN-1 (each target depends on the previous one).
func buildChainGraph(n int) (*pb.Metadata, map[string]*pb.OptimizedTarget) {
	meta := &pb.Metadata{TargetIdMapping: make(map[int32]string, n)}
	targetsByName := make(map[string]*pb.OptimizedTarget, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("//app:T%d", i)
		meta.TargetIdMapping[int32(i)] = name
		t := &pb.OptimizedTarget{Id: int32(i)}
		if i > 0 {
			t.DirectDependencies = []int32{int32(i - 1)}
		}
		targetsByName[name] = t
	}
	return meta, targetsByName
}

// buildBenchGraph creates a chain of n targets packed into a single
// GetTargetGraphResponse slice, used as input for compareTargetGraphs.
func buildBenchGraph(n int, hashSuffix string) []*pb.GetTargetGraphResponse {
	meta := &pb.Metadata{
		TargetIdMapping: make(map[int32]string, n),
		RuleTypeMapping: map[int32]string{1: "go_library"},
	}
	targets := make([]*pb.OptimizedTarget, n)
	for i := 0; i < n; i++ {
		meta.TargetIdMapping[int32(i)] = fmt.Sprintf("//app:T%d", i)
		t := &pb.OptimizedTarget{
			Id:       int32(i),
			Hash:     fmt.Sprintf("hash_%d_%s", i, hashSuffix),
			RuleType: 1,
		}
		if i > 0 {
			t.DirectDependencies = []int32{int32(i - 1)}
		}
		targets[i] = t
	}
	return []*pb.GetTargetGraphResponse{
		{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{Targets: targets},
			},
		},
		{
			Item: &pb.GetTargetGraphResponse_Metadata{Metadata: meta},
		},
	}
}
