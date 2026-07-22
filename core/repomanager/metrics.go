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

package repomanager

import (
	"time"

	"github.com/uber-go/tally"
	"github.com/uber/tango/observability/metrics"
)

// Metric op and per-step names for RepoManager.Lease.
const (
	_opLease = "lease"

	_stepEnsureOrigin = "ensure_origin_duration"
	_stepWaitSlot     = "wait_slot_duration"
	_stepCreateWorker = "create_worker_duration"
)

// _stepDurationBuckets spans a slot wait or clone: exponential 1ms..~1.3h.
var _stepDurationBuckets = tally.MustMakeExponentialDurationBuckets(time.Millisecond, 3, 15)

func recordStep(e *metrics.Emitter, name string, start time.Time) {
	e.DurationHistogram(_opLease, name, _stepDurationBuckets).RecordDuration(time.Since(start))
}
