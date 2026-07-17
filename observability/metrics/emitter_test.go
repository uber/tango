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
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber-go/tally"
)

func TestNew(t *testing.T) {
	t.Run("nil scope returns error", func(t *testing.T) {
		e, err := New(nil)
		require.Error(t, err)
		assert.Nil(t, e)
	})

	t.Run("valid scope succeeds", func(t *testing.T) {
		e, err := New(tally.NoopScope)
		require.NoError(t, err)
		assert.NotNil(t, e)
	})
}

func TestNop(t *testing.T) {
	e := Nop()
	require.NotNil(t, e)
	// Should not panic.
	e.Counter("op", "c").Inc(1)
}

func TestCounter(t *testing.T) {
	ts := tally.NewTestScope("pfx", nil)
	e, err := New(ts)
	require.NoError(t, err)

	e.Counter("my_op", "start").Inc(1)

	counters := ts.Snapshot().Counters()
	require.Contains(t, counters, "pfx.my_op.start+")
	assert.Equal(t, int64(1), counters["pfx.my_op.start+"].Value())
}

func TestDurationHistogram(t *testing.T) {
	ts := tally.NewTestScope("pfx", nil)
	e, err := New(ts)
	require.NoError(t, err)

	buckets := tally.MustMakeLinearDurationBuckets(0, time.Millisecond, 10)
	e.DurationHistogram("my_op", "finish", buckets).RecordDuration(time.Millisecond)

	require.Contains(t, ts.Snapshot().Histograms(), "pfx.my_op.finish+")
}

func TestValueHistogram(t *testing.T) {
	ts := tally.NewTestScope("pfx", nil)
	e, err := New(ts)
	require.NoError(t, err)

	buckets := tally.MustMakeLinearValueBuckets(0, 10, 5)
	e.ValueHistogram("my_op", "target_count", buckets).RecordValue(42)

	require.Contains(t, ts.Snapshot().Histograms(), "pfx.my_op.target_count+")
}

func TestTagged(t *testing.T) {
	t.Run("empty tags returns same emitter", func(t *testing.T) {
		e := Nop()
		assert.Same(t, e, e.Tagged(nil))
		assert.Same(t, e, e.Tagged(map[string]string{}))
	})

	t.Run("tags appear on instruments", func(t *testing.T) {
		ts := tally.NewTestScope("pfx", nil)
		e, err := New(ts)
		require.NoError(t, err)

		e.Tagged(map[string]string{"repo": "myrepo"}).Counter("my_op", "start").Inc(1)

		counters := ts.Snapshot().Counters()
		require.Contains(t, counters, "pfx.my_op.start+repo=myrepo")
		assert.Equal(t, int64(1), counters["pfx.my_op.start+repo=myrepo"].Value())
	})

	t.Run("parent is not affected by child tags", func(t *testing.T) {
		ts := tally.NewTestScope("pfx", nil)
		e, err := New(ts)
		require.NoError(t, err)

		e.Tagged(map[string]string{"repo": "myrepo"}).Counter("op", "c").Inc(1)
		e.Counter("op", "c").Inc(1)

		counters := ts.Snapshot().Counters()
		assert.Contains(t, counters, "pfx.op.c+repo=myrepo")
		assert.Contains(t, counters, "pfx.op.c+")
	})

	t.Run("tags compose across calls", func(t *testing.T) {
		ts := tally.NewTestScope("pfx", nil)
		e, err := New(ts)
		require.NoError(t, err)

		e.Tagged(map[string]string{"repo": "myrepo"}).
			Tagged(map[string]string{"result": "success"}).
			Counter("op", "finish").Inc(1)

		assert.Contains(t, ts.Snapshot().Counters(), "pfx.op.finish+repo=myrepo,result=success")
	})
}

func TestBeginComplete(t *testing.T) {
	buckets := tally.MustMakeLinearDurationBuckets(0, time.Millisecond, 10)

	tests := []struct {
		name       string
		err        error
		wantResult string
	}{
		{"success", nil, ResultSuccess},
		{"failure", fmt.Errorf("boom"), ResultFailure},
		{"cancelled", context.Canceled, ResultCancelled},
		{"timeout is failure", context.DeadlineExceeded, ResultFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := tally.NewTestScope("pfx", nil)
			e, err := New(ts)
			require.NoError(t, err)

			op := Begin(e, "my_op", buckets)
			op.Complete(tt.err)

			snap := ts.Snapshot()
			assert.Contains(t, snap.Counters(), "pfx.my_op.start+")
			assert.Contains(t, snap.Histograms(), "pfx.my_op.finish+result="+tt.wantResult)
		})
	}
}

func TestOutcome(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ResultSuccess},
		{"generic error", fmt.Errorf("boom"), ResultFailure},
		{"context canceled", context.Canceled, ResultCancelled},
		{"wrapped canceled", fmt.Errorf("op: %w", context.Canceled), ResultCancelled},
		{"deadline exceeded is failure", context.DeadlineExceeded, ResultFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Outcome(tt.err))
		})
	}
}
