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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber-go/tally"
)

func TestNewNilScopeIsNoop(t *testing.T) {
	e := New(nil)
	require.NotNil(t, e)
	e.Inc("op", "n")
	e.Gauge("op", "n", 1)
	e.RecordDur("op", "n", time.Millisecond)
	e.RecordCount("op", "n", 42)
}

func TestIncEmitsUnderOpSubscope(t *testing.T) {
	s := tally.NewTestScope("root", nil)
	e := New(s)
	e.Inc("get_target_graph", "requests")
	assert.Equal(t, int64(1), counterValue(t, s, "root.get_target_graph.requests", map[string]string{}))
}

func TestIncMergesTags(t *testing.T) {
	s := tally.NewTestScope("", nil)
	e := New(s).Tagged(map[string]string{TagEmitter: "service", TagRepo: "acme/monorepo"})
	e.Inc("op", "requests", WithTags(map[string]string{TagResult: string(ResultSuccess)}))
	assert.Equal(t, int64(1), counterValue(t, s, "op.requests", map[string]string{
		TagEmitter: "service",
		TagRepo:    "acme/monorepo",
		TagResult:  string(ResultSuccess),
	}))
	e.Inc("op", "requests",
		WithTags(map[string]string{TagResult: string(ResultSuccess)}),
		WithTags(map[string]string{TagResult: string(ResultFail)}),
	)
	assert.Equal(t, int64(1), counterValue(t, s, "op.requests", map[string]string{
		TagEmitter: "service",
		TagRepo:    "acme/monorepo",
		TagResult:  string(ResultFail),
	}))
}

func TestTaggedDoesNotMutateParent(t *testing.T) {
	s := tally.NewTestScope("", nil)
	parent := New(s).Tagged(map[string]string{TagEmitter: "service"})
	child := parent.Tagged(map[string]string{TagRepo: "acme"})

	child.Inc("op", "requests")
	parent.Inc("op", "requests")

	assert.Equal(t, int64(1), counterValue(t, s, "op.requests", map[string]string{
		TagEmitter: "service",
		TagRepo:    "acme",
	}))
	assert.Equal(t, int64(1), counterValue(t, s, "op.requests", map[string]string{
		TagEmitter: "service",
	}))
}

func TestTaggedEmptyReturnsSameEmitter(t *testing.T) {
	e := New(nil)
	assert.Same(t, e, e.Tagged(nil))
	assert.Same(t, e, e.Tagged(map[string]string{}))
}

func TestGaugeUpdatesValue(t *testing.T) {
	s := tally.NewTestScope("", nil)
	e := New(s)
	e.Gauge("op", "in_flight_requests", 3, WithTags(map[string]string{TagOperation: "op"}))
	v, ok := gaugeValue(t, s, "op.in_flight_requests", map[string]string{TagOperation: "op"})
	require.True(t, ok)
	assert.Equal(t, float64(3), v)
}

func TestRecordDurUsesDefaultBuckets(t *testing.T) {
	s := tally.NewTestScope("", nil)
	e := New(s)
	e.RecordDur("op", "total_duration", 5*time.Millisecond)
	assert.Equal(t, 1, histogramDurationSamples(t, s, "op.total_duration", map[string]string{}))
}

func TestRecordDurBucketsOverride(t *testing.T) {
	custom := tally.MustMakeLinearDurationBuckets(time.Millisecond, time.Millisecond, 5)
	s := tally.NewTestScope("", nil)
	e := New(s)
	e.RecordDur("op", "n", 2*time.Millisecond, WithDurationBuckets(custom))
	assert.Equal(t, 1, histogramDurationSamples(t, s, "op.n", map[string]string{}))
}

func TestRecordCountUsesDefaultBuckets(t *testing.T) {
	s := tally.NewTestScope("", nil)
	e := New(s)
	e.RecordCount("op", "targets_count", 128)
	assert.Equal(t, 1, histogramValueSamples(t, s, "op.targets_count", map[string]string{}))
}

func TestRecordCountBucketsOverride(t *testing.T) {
	custom := tally.MustMakeLinearValueBuckets(0, 1, 5)
	s := tally.NewTestScope("", nil)
	e := New(s)
	e.RecordCount("op", "n", 2, WithValueBuckets(custom))
	assert.Equal(t, 1, histogramValueSamples(t, s, "op.n", map[string]string{}))
}

func TestTrackInFlightBalances(t *testing.T) {
	s := tally.NewTestScope("", nil)
	e := New(s)
	release1 := e.TrackInFlight(OpGetTargetGraph)
	release2 := e.TrackInFlight(OpGetTargetGraph)

	v, ok := gaugeValue(t, s, InFlightRequests, map[string]string{TagOperation: OpGetTargetGraph})
	require.True(t, ok)
	assert.Equal(t, float64(2), v)

	release1()
	release2()

	v, _ = gaugeValue(t, s, InFlightRequests, map[string]string{TagOperation: OpGetTargetGraph})
	assert.Equal(t, float64(0), v)
}

func TestTrackInFlightSeparateOps(t *testing.T) {
	s := tally.NewTestScope("", nil)
	e := New(s)
	defer e.TrackInFlight(OpGetTargetGraph)()
	defer e.TrackInFlight(OpGetChangedTargets)()
	defer e.TrackInFlight(OpGetChangedTargets)()

	v1, _ := gaugeValue(t, s, InFlightRequests, map[string]string{TagOperation: OpGetTargetGraph})
	v2, _ := gaugeValue(t, s, InFlightRequests, map[string]string{TagOperation: OpGetChangedTargets})
	assert.Equal(t, float64(1), v1)
	assert.Equal(t, float64(2), v2)
}

// A Tagged child must share the parent's counter — otherwise gauges leak if a
// handler tags between the increment and the deferred release.
func TestTrackInFlightSharedAcrossTaggedChildren(t *testing.T) {
	s := tally.NewTestScope("", nil)
	parent := New(s)
	child := parent.Tagged(map[string]string{TagRepo: "acme"})

	release := parent.TrackInFlight(OpGetTargetGraph)
	release2 := child.TrackInFlight(OpGetTargetGraph)

	v, _ := gaugeValue(t, s, InFlightRequests, map[string]string{TagOperation: OpGetTargetGraph})
	assert.Equal(t, float64(2), v)

	release()
	release2()

	v, _ = gaugeValue(t, s, InFlightRequests, map[string]string{TagOperation: OpGetTargetGraph})
	assert.Equal(t, float64(0), v)
}

func TestTrackInFlightConcurrent(t *testing.T) {
	s := tally.NewTestScope("", nil)
	e := New(s)

	const workers = 32
	const iterations = 500

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				e.TrackInFlight(OpGetTargetGraph)()
			}
		}()
	}
	wg.Wait()

	v, _ := gaugeValue(t, s, InFlightRequests, map[string]string{TagOperation: OpGetTargetGraph})
	assert.Equal(t, float64(0), v)
}

func counterValue(t *testing.T, s tally.TestScope, name string, tags map[string]string) int64 {
	t.Helper()
	for _, c := range s.Snapshot().Counters() {
		if c.Name() == name && tagsEqual(c.Tags(), tags) {
			return c.Value()
		}
	}
	return 0
}

func gaugeValue(t *testing.T, s tally.TestScope, name string, tags map[string]string) (float64, bool) {
	t.Helper()
	for _, g := range s.Snapshot().Gauges() {
		if g.Name() == name && tagsEqual(g.Tags(), tags) {
			return g.Value(), true
		}
	}
	return 0, false
}

func histogramDurationSamples(t *testing.T, s tally.TestScope, name string, tags map[string]string) int {
	t.Helper()
	total := 0
	for _, h := range s.Snapshot().Histograms() {
		if h.Name() != name || !tagsEqual(h.Tags(), tags) {
			continue
		}
		for _, n := range h.Durations() {
			total += int(n)
		}
	}
	return total
}

func histogramValueSamples(t *testing.T, s tally.TestScope, name string, tags map[string]string) int {
	t.Helper()
	total := 0
	for _, h := range s.Snapshot().Histograms() {
		if h.Name() != name || !tagsEqual(h.Tags(), tags) {
			continue
		}
		for _, n := range h.Values() {
			total += int(n)
		}
	}
	return total
}

func tagsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
