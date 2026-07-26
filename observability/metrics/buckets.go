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

package metrics

import (
	"time"

	"github.com/uber-go/tally"
)

// Shared, production-tuned bucket sets. They live here (rather than per package)
// so the same work is comparable across nesting layers — a controller finish,
// the orchestrator step it's dominated by, and the graphrunner phase under that
// all land on the same edges.
var (
	// FastDurationBuckets covers sub-operations from milliseconds up to a few
	// minutes (sends, cache reads, decode, diff, checkout, file hashing, slot
	// waits, local clones). Fine-grained (1.4×) so both sub-second latencies and
	// multi-minute steps are distinguishable: exponential 1ms..~12m.
	FastDurationBuckets = tally.MustMakeExponentialDurationBuckets(time.Millisecond, 1.4, 41)

	// SlowDurationBuckets covers whole-operation finishes and minutes-to-hours
	// work (graph fetch/compute, bazel query, origin clone). 3–20 min at 15 s
	// resolution (69 buckets), with coarse edges outside so short and outlier
	// requests still bucket somewhere.
	SlowDurationBuckets = append(append(
		tally.DurationBuckets{
			time.Second, 10 * time.Second, 30 * time.Second,
			time.Minute, 2 * time.Minute,
		},
		tally.MustMakeLinearDurationBuckets(3*time.Minute, 15*time.Second, 69)...),
		25*time.Minute, 30*time.Minute, 40*time.Minute, 45*time.Minute, time.Hour, 2*time.Hour, 4*time.Hour, 6*time.Hour,
	)

	// LargeCountBuckets covers large item counts such as target and
	// changed-target counts. Graphs already reach ~4M targets: 100..~4M,
	// fine-grained exponential (~1.115×) above 1k.
	LargeCountBuckets = append(
		tally.ValueBuckets{100, 500},
		tally.MustMakeExponentialValueBuckets(1_000, 1.115, 78)...,
	)
)
