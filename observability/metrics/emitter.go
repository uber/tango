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

// Package metrics is a thin wrapper over tally.Scope that pins Tango's metric
// conventions in one place:
//
//   - every metric is emitted under an operation subscope (e.g. get_target_graph)
//     so related series share a prefix;
//   - durations and counts use the shared default histogram buckets unless a
//     call overrides them via WithDurationBuckets / WithValueBuckets;
//   - each emission carries the emitter's base tags merged with any per-call
//     tags, with per-call tags winning on collision.
//
// The metric, tag, and result-value names are defined in names.go.
package metrics

import (
	"time"

	"github.com/uber-go/tally"
)

// Emitter emits metrics under a per-operation subscope with a fixed set of
// base tags. It is safe for concurrent use.
type Emitter struct {
	scope           tally.Scope
	baseTags        map[string]string
	durationBuckets tally.DurationBuckets
	valueBuckets    tally.ValueBuckets
}

// New returns a tally-backed Emitter. A nil scope falls back to
// tally.NoopScope so callers do not need to special-case unwired metrics.
func New(scope tally.Scope) *Emitter {
	if scope == nil {
		scope = tally.NoopScope
	}
	return &Emitter{
		scope:           scope,
		durationBuckets: _defaultDurationBuckets,
		valueBuckets:    _defaultCountBuckets,
	}
}

// Tagged returns a child Emitter that adds tags to every emission. The receiver
// is left unchanged; passing no tags returns the receiver.
func (e *Emitter) Tagged(tags map[string]string) *Emitter {
	if len(tags) == 0 {
		return e
	}
	return &Emitter{
		scope:           e.scope,
		baseTags:        mergeTags(e.baseTags, tags),
		durationBuckets: e.durationBuckets,
		valueBuckets:    e.valueBuckets,
	}
}

// Inc increments the named counter under the op subscope by one.
func (e *Emitter) Inc(op, name string, opts ...Option) {
	o := applyOptions(opts)
	e.scope.SubScope(op).Tagged(mergeTags(e.baseTags, o.tags)).Counter(name).Inc(1)
}

// Gauge sets the named gauge under the op subscope to v.
func (e *Emitter) Gauge(op, name string, v float64, opts ...Option) {
	o := applyOptions(opts)
	e.scope.SubScope(op).Tagged(mergeTags(e.baseTags, o.tags)).Gauge(name).Update(v)
}

// RecordDur records d in the named histogram under the op subscope, using the
// default duration buckets unless overridden with WithDurationBuckets.
func (e *Emitter) RecordDur(op, name string, d time.Duration, opts ...Option) {
	o := applyOptions(opts)
	buckets := o.durationBuckets
	if buckets == nil {
		buckets = e.durationBuckets
	}
	e.scope.SubScope(op).Tagged(mergeTags(e.baseTags, o.tags)).Histogram(name, buckets).RecordDuration(d)
}

// RecordCount records v in the named histogram under the op subscope, using the
// default value buckets unless overridden with WithValueBuckets.
func (e *Emitter) RecordCount(op, name string, v int64, opts ...Option) {
	o := applyOptions(opts)
	buckets := o.valueBuckets
	if buckets == nil {
		buckets = e.valueBuckets
	}
	e.scope.SubScope(op).Tagged(mergeTags(e.baseTags, o.tags)).Histogram(name, buckets).RecordValue(float64(v))
}

// mergeTags returns the union of base and extra, with extra winning on key
// collision. It returns nil when both are empty so callers pass no tags
// through to tally.
func mergeTags(base, extra map[string]string) map[string]string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	merged := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	return merged
}
