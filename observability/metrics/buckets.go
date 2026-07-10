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

// _defaultDurationBuckets spans ~1ms to ~18h (1ms · 2^26) across 27 exponential
// buckets so that both sub-second RPCs and multi-hour graph builds land in a
// meaningful bucket. Callers override per call with WithDurationBuckets.
var _defaultDurationBuckets = tally.MustMakeExponentialDurationBuckets(time.Millisecond, 2.0, 27)

// _defaultCountBuckets spans 1 to ~8M (1 · 2^23) across 24 exponential buckets
// to cover target/graph cardinalities. Callers override with WithValueBuckets.
var _defaultCountBuckets = tally.MustMakeExponentialValueBuckets(1.0, 2.0, 24)
