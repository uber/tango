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

package orchestrator

import (
	"time"

	"github.com/uber-go/tally"
)

// opGetTargetGraph is snake_cased after the Orchestrator.GetTargetGraph method.
const _opGetTargetGraph = "get_target_graph"

// _stepDurationBuckets covers whole-operation and individual pipeline steps
// (lease, checkout, apply, cache read/write, compute). A compute on a large
// repo can run for hours, so the range extends to ~4h: exponential 1ms..~4h.
var _stepDurationBuckets = tally.MustMakeExponentialDurationBuckets(time.Millisecond, 3, 16)
