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
	"time"

	"github.com/uber-go/tally"
)

// Operation names, snake_cased after the RPC interface methods they measure.
const (
	opGetTargetGraph        = "get_target_graph"
	opGetChangedTargets     = "get_changed_targets"
	opGetChangedTargetGraph = "get_changed_target_graph"
	opCompareTargetGraphs   = "compare_target_graphs"
)

// Metric buckets for the controller's operations. Per the observability/metrics
// design, buckets are declared as package-level values next to the handlers and
// passed to Begin (finish) or the custom-metric callsites.
var (
	// fastDurationBuckets covers cheap sub-operations that are normally
	// milliseconds to seconds (sends, cache reads, decode, diff). Fine-grained
	// so sub-second latencies are distinguishable: exponential 1ms..~35m.
	fastDurationBuckets = tally.MustMakeExponentialDurationBuckets(time.Millisecond, 2, 22)

	// slowDurationBuckets covers whole-operation finishes and hours-scale work
	// (graph fetch, download). The edges match the orchestrator and graphrunner
	// slow buckets so the same compute is comparable across nesting layers:
	// exponential 1ms..~12h.
	slowDurationBuckets = tally.MustMakeExponentialDurationBuckets(time.Millisecond, 3, 17)

	// changedTargetCountBuckets covers changed-target counts. Graphs already
	// reach ~4M targets, so a diff can be large: exponential 1..~100M.
	changedTargetCountBuckets = tally.MustMakeExponentialValueBuckets(1, 10, 9)
)
