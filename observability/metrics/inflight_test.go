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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber-go/tally"
)

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
