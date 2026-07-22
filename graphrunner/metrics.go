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

package graphrunner

import (
	"time"

	"github.com/uber-go/tally"
)

// opCompute is snake_cased after the GraphRunner.Compute method.
const _opCompute = "compute"

var (
	// _phaseDurationBuckets covers the compute phases (bazel query, git file
	// hashes, target hashing). A bazel query on a large repo can run for hours,
	// so the range extends to ~4.9h: exponential 100ms..~4.9h.
	_phaseDurationBuckets = tally.MustMakeExponentialDurationBuckets(100*time.Millisecond, 3, 12)

	// _targetCountBuckets covers the computed graph size. Graphs already reach
	// ~4M targets: exponential 1..~100M.
	_targetCountBuckets = tally.MustMakeExponentialValueBuckets(1, 10, 9)
)
