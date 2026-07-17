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

// Benchmarks for GetChangedTargets against fixed, checked-in commit pairs of
// increasing diff size. These are for local performance measurement only:
// run via `make bench`, never via `make test` / `make test-integration`,
// and never from CI. They assert nothing about how fast the call must be.
package integration_test

import (
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func runChangedTargetsBenchmark(b *testing.B, name, firstSHA, secondSHA string) {
	remote := repoRemote(b)
	logger := zap.New(zapcore.NewNopCore())
	addr := startServerWithLogger(b, remote, logger)
	client := newClient(b, addr)

	coldStart := time.Now()
	getChangedTargets(b, client, remote, firstSHA, secondSHA)
	coldDuration := time.Since(coldStart)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		getChangedTargets(b, client, remote, firstSHA, secondSHA)
	}
	cachedDuration := b.Elapsed() / time.Duration(b.N)

	b.ReportMetric(coldDuration.Seconds(), "first_uncached_call_sec")
	b.ReportMetric(cachedDuration.Seconds(), "avg_cached_call_sec/op")
}

func BenchmarkGetChangedTargets_SmallDiff(b *testing.B) {
	// 5716262 -> 74d1cd5: 2 files changed, 13 lines.
	runChangedTargetsBenchmark(b, "SmallDiff", "57162624a45965a7e783072c56561f91c5d4084d", "74d1cd55155e5f4f43aa92b4e0146a0c528a0d96")
}

func BenchmarkGetChangedTargets_MediumDiff(b *testing.B) {
	// 046de2c -> 1f2e3e9: 8 files changed, 461 lines.
	runChangedTargetsBenchmark(b, "MediumDiff", "046de2c20b5492cd5606d32fd632a38b8b70c8f6", "1f2e3e9245b159006cf2103becd51c5c1b6ec868")
}

func BenchmarkGetChangedTargets_LargeDiff(b *testing.B) {
	// 684a6d0 (repo setup) -> 9bf997c: 162 files changed, ~19.9k lines.
	runChangedTargetsBenchmark(b, "LargeDiff", "684a6d06e82cff50a4f1a5addb8f64a45aaa6c26", "9bf997cf709b1ee38a71fa0d1423e66d0494e9bc")
}
