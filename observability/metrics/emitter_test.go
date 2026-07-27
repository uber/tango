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
	tangoerrors "github.com/uber/tango/core/errors"
)

func TestNew(t *testing.T) {
	t.Run("nil scope falls back to no-op", func(t *testing.T) {
		e := New(nil)
		require.NotNil(t, e)
		e.Counter("op", "c").Inc(1) // must not panic
	})

	t.Run("valid scope", func(t *testing.T) {
		e := New(tally.NoopScope)
		require.NotNil(t, e)
	})
}

func TestSubScope(t *testing.T) {
	ts := tally.NewTestScope("pfx", nil)
	New(ts).SubScope("child").Counter("op", "c").Inc(1)
	require.Contains(t, ts.Snapshot().Counters(), "pfx.child.op.c+")
}

func TestNop(t *testing.T) {
	e := Nop()
	require.NotNil(t, e)
	// Should not panic.
	e.Counter("op", "c").Inc(1)
}

func TestCounter(t *testing.T) {
	ts := tally.NewTestScope("pfx", nil)
	e := New(ts)

	e.Counter("my_op", "start").Inc(1)

	counters := ts.Snapshot().Counters()
	require.Contains(t, counters, "pfx.my_op.start+")
	assert.Equal(t, int64(1), counters["pfx.my_op.start+"].Value())
}

func TestDurationHistogram(t *testing.T) {
	ts := tally.NewTestScope("pfx", nil)
	e := New(ts)

	buckets := tally.MustMakeLinearDurationBuckets(0, time.Millisecond, 10)
	e.DurationHistogram("my_op", "finish", buckets).RecordDuration(time.Millisecond)

	require.Contains(t, ts.Snapshot().Histograms(), "pfx.my_op.finish+")
}

func TestValueHistogram(t *testing.T) {
	ts := tally.NewTestScope("pfx", nil)
	e := New(ts)

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
		e := New(ts)

		e.Tagged(map[string]string{"repo": "myrepo"}).Counter("my_op", "start").Inc(1)

		counters := ts.Snapshot().Counters()
		require.Contains(t, counters, "pfx.my_op.start+repo=myrepo")
		assert.Equal(t, int64(1), counters["pfx.my_op.start+repo=myrepo"].Value())
	})

	t.Run("parent is not affected by child tags", func(t *testing.T) {
		ts := tally.NewTestScope("pfx", nil)
		e := New(ts)

		e.Tagged(map[string]string{"repo": "myrepo"}).Counter("op", "c").Inc(1)
		e.Counter("op", "c").Inc(1)

		counters := ts.Snapshot().Counters()
		assert.Contains(t, counters, "pfx.op.c+repo=myrepo")
		assert.Contains(t, counters, "pfx.op.c+")
	})

	t.Run("tags compose across calls", func(t *testing.T) {
		ts := tally.NewTestScope("pfx", nil)
		e := New(ts)

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
		{"infra error", fmt.Errorf("boom"), "infra"},
		{"cancelled", context.Canceled, "cancelled"},
		{"timeout is infra", context.DeadlineExceeded, "infra"},
		{"user error", tangoerrors.NewUser(fmt.Errorf("bad input")), "user"},
		{"infra retryable", tangoerrors.NewInfraRetryable(fmt.Errorf("transient")), "infra_retryable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := tally.NewTestScope("pfx", nil)
			e := New(ts)

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
		{"generic error", fmt.Errorf("boom"), "infra"},
		{"context canceled", context.Canceled, "cancelled"},
		{"wrapped canceled", fmt.Errorf("op: %w", context.Canceled), "cancelled"},
		{"deadline exceeded is infra", context.DeadlineExceeded, "infra"},
		{"user error", tangoerrors.NewUser(fmt.Errorf("bad")), "user"},
		{"infra retryable", tangoerrors.NewInfraRetryable(fmt.Errorf("retry")), "infra_retryable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Outcome(tt.err))
		})
	}
}
