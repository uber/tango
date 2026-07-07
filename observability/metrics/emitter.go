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

// Package metrics is a thin wrapper over tally.Scope that pins the naming,
// bucket, and tag conventions defined in the Tango Metrics Inventory.
package metrics

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/uber-go/tally"
)

// Emitter is the interface for emitting metrics.
type Emitter interface {
	Inc(op, name string, opts ...Option)
	Gauge(op, name string, v float64, opts ...Option)
	RecordDur(op, name string, d time.Duration, opts ...Option)
	RecordCount(op, name string, v int64, opts ...Option)
	TrackInFlight(op string) func()
	Tagged(tags map[string]string) Emitter
}

type defaultEmitter struct {
	scope           tally.Scope
	baseTags        map[string]string
	durationBuckets tally.DurationBuckets
	valueBuckets    tally.ValueBuckets
	inFlight        *sync.Map
}

// New returns a tally-backed Emitter. A nil scope falls back to
// tally.NoopScope so callers do not need to special-case unwired metrics.
func New(scope tally.Scope) Emitter {
	if scope == nil {
		scope = tally.NoopScope
	}
	return &defaultEmitter{
		scope:           scope,
		durationBuckets: _defaultDurationBuckets,
		valueBuckets:    _defaultCountBuckets,
		inFlight:        &sync.Map{},
	}
}

func (e *defaultEmitter) Tagged(tags map[string]string) Emitter {
	if len(tags) == 0 {
		return e
	}
	merged := make(map[string]string, len(e.baseTags)+len(tags))
	for k, v := range e.baseTags {
		merged[k] = v
	}
	for k, v := range tags {
		merged[k] = v
	}
	return &defaultEmitter{
		scope:           e.scope,
		baseTags:        merged,
		durationBuckets: e.durationBuckets,
		valueBuckets:    e.valueBuckets,
		inFlight:        e.inFlight,
	}
}

func (e *defaultEmitter) Inc(op, name string, opts ...Option) {
	o := e.applyOptions(opts)
	e.subscope(op, o.tags).Counter(name).Inc(1)
}

func (e *defaultEmitter) Gauge(op, name string, v float64, opts ...Option) {
	o := e.applyOptions(opts)
	e.subscope(op, o.tags).Gauge(name).Update(v)
}

func (e *defaultEmitter) RecordDur(op, name string, d time.Duration, opts ...Option) {
	o := e.applyOptions(opts)
	buckets := o.durationBuckets
	if buckets == nil {
		buckets = e.durationBuckets
	}
	e.subscope(op, o.tags).Histogram(name, buckets).RecordDuration(d)
}

func (e *defaultEmitter) RecordCount(op, name string, v int64, opts ...Option) {
	o := e.applyOptions(opts)
	buckets := o.valueBuckets
	if buckets == nil {
		buckets = e.valueBuckets
	}
	e.subscope(op, o.tags).Histogram(name, buckets).RecordValue(float64(v))
}

func (e *defaultEmitter) subscope(op string, callTags map[string]string) tally.Scope {
	s := e.scope.SubScope(op)
	if len(e.baseTags) == 0 && len(callTags) == 0 {
		return s
	}
	merged := make(map[string]string, len(e.baseTags)+len(callTags))
	for k, v := range e.baseTags {
		merged[k] = v
	}
	for k, v := range callTags {
		merged[k] = v
	}
	return s.Tagged(merged)
}

func (e *defaultEmitter) applyOptions(opts []Option) emitOpts {
	var o emitOpts
	for _, opt := range opts {
		opt.apply(&o)
	}
	return o
}

func (e *defaultEmitter) TrackInFlight(op string) func() {
	v, _ := e.inFlight.LoadOrStore(op, new(int64))
	counter := v.(*int64)
	e.emitInFlight(op, atomic.AddInt64(counter, 1))
	return func() {
		e.emitInFlight(op, atomic.AddInt64(counter, -1))
	}
}

func (e *defaultEmitter) emitInFlight(op string, n int64) {
	e.scope.Tagged(map[string]string{TagOperation: op}).Gauge(InFlightRequests).Update(float64(n))
}
