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

// Package metrics is a thin, concrete wrapper over tally.Scope that pins the
// metric path shape to <scope>.<op>.<name> and provides Begin/Complete
// lifecycle helpers that emit a start counter and a result-tagged finish
// duration histogram for every instrumented operation.
package metrics

import (
	"time"

	"github.com/uber-go/tally"
)

// Emitter is a concrete, Tally-backed metrics emitter that pins the metric
// path shape to <scope>.<op>.<name>.
type Emitter struct {
	scope tally.Scope
}

// New creates an Emitter backed by the given tally.Scope. A nil scope falls
// back to a no-op scope, so New always returns a usable Emitter.
func New(scope tally.Scope) *Emitter {
	if scope == nil {
		scope = tally.NoopScope
	}
	return &Emitter{scope: scope}
}

// Nop returns an Emitter that discards all metrics.
func Nop() *Emitter { return &Emitter{scope: tally.NoopScope} }

// SubScope returns a child Emitter rooted at a nested scope segment.
func (e *Emitter) SubScope(name string) *Emitter {
	return &Emitter{scope: e.scope.SubScope(name)}
}

// Tagged returns a child Emitter whose instruments carry the given tags in
// addition to any already on the scope. It delegates to tally.Scope.Tagged.
func (e *Emitter) Tagged(tags map[string]string) *Emitter {
	if len(tags) == 0 {
		return e
	}
	return &Emitter{scope: e.scope.Tagged(tags)}
}

// Counter returns a counter at <scope>.<op>.<name>.
func (e *Emitter) Counter(op, name string) tally.Counter {
	return e.scope.SubScope(op).Counter(name)
}

// DurationHistogram returns a duration histogram at <scope>.<op>.<name>.
func (e *Emitter) DurationHistogram(op, name string, b tally.DurationBuckets) tally.Histogram {
	return e.scope.SubScope(op).Histogram(name, b)
}

// ValueHistogram returns a value histogram at <scope>.<op>.<name>.
func (e *Emitter) ValueHistogram(op, name string, b tally.ValueBuckets) tally.Histogram {
	return e.scope.SubScope(op).Histogram(name, b)
}

// Op is an in-flight operation started by Begin. Complete records its outcome.
type Op struct {
	emitter *Emitter
	op      string
	buckets tally.DurationBuckets
	start   time.Time
}

// Begin records the start counter for op on e and returns a handle whose
// Complete records the finish histogram. e carries the repo tag (and any
// other stable tags) baked in by the caller.
func Begin(e *Emitter, op string, buckets tally.DurationBuckets) *Op {
	e.Counter(op, "start").Inc(1)
	return &Op{emitter: e, op: op, buckets: buckets, start: time.Now()}
}

// Complete records the finish duration histogram tagged with the outcome.
func (o *Op) Complete(err error) {
	o.emitter.
		Tagged(map[string]string{TagResult: Outcome(err)}).
		DurationHistogram(o.op, "finish", o.buckets).
		RecordDuration(time.Since(o.start))
}
