package metrics

import (
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

	snap := ts.Snapshot()
	counters := snap.Counters()
	require.Contains(t, counters, "pfx.my_op.start+")
	assert.Equal(t, int64(1), counters["pfx.my_op.start+"].Value())
}

func TestDurationHistogram(t *testing.T) {
	ts := tally.NewTestScope("pfx", nil)
	e, err := New(ts)
	require.NoError(t, err)

	buckets := tally.MustMakeLinearDurationBuckets(0, time.Millisecond, 10)
	e.DurationHistogram("my_op", "finish", buckets).RecordDuration(time.Millisecond)

	snap := ts.Snapshot()
	histograms := snap.Histograms()
	require.Contains(t, histograms, "pfx.my_op.finish+")
}

func TestValueHistogram(t *testing.T) {
	ts := tally.NewTestScope("pfx", nil)
	e, err := New(ts)
	require.NoError(t, err)

	buckets := tally.MustMakeLinearValueBuckets(0, 10, 5)
	e.ValueHistogram("my_op", "target_counts", buckets).RecordValue(42)

	snap := ts.Snapshot()
	histograms := snap.Histograms()
	require.Contains(t, histograms, "pfx.my_op.target_counts+")
}

func TestTagged(t *testing.T) {
	t.Run("empty tags returns same emitter", func(t *testing.T) {
		e := Nop()
		child := e.Tagged(nil)
		assert.Same(t, e, child)

		child = e.Tagged(map[string]string{})
		assert.Same(t, e, child)
	})

	t.Run("tags appear on instruments", func(t *testing.T) {
		ts := tally.NewTestScope("pfx", nil)
		e, err := New(ts)
		require.NoError(t, err)

		tagged := e.Tagged(map[string]string{"repo": "myrepo"})
		tagged.Counter("my_op", "start").Inc(1)

		snap := ts.Snapshot()
		counters := snap.Counters()
		require.Contains(t, counters, "pfx.my_op.start+repo=myrepo")
		assert.Equal(t, int64(1), counters["pfx.my_op.start+repo=myrepo"].Value())
	})

	t.Run("parent is not affected by child tags", func(t *testing.T) {
		ts := tally.NewTestScope("pfx", nil)
		e, err := New(ts)
		require.NoError(t, err)

		e.Tagged(map[string]string{"repo": "myrepo"}).Counter("op", "c").Inc(1)
		e.Counter("op", "c").Inc(1)

		snap := ts.Snapshot()
		counters := snap.Counters()
		assert.Contains(t, counters, "pfx.op.c+repo=myrepo")
		assert.Contains(t, counters, "pfx.op.c+")
	})

	t.Run("tags compose across calls", func(t *testing.T) {
		ts := tally.NewTestScope("pfx", nil)
		e, err := New(ts)
		require.NoError(t, err)

		child := e.Tagged(map[string]string{"repo": "myrepo"})
		grandchild := child.Tagged(map[string]string{"result": "success"})
		grandchild.Counter("op", "finish").Inc(1)

		snap := ts.Snapshot()
		counters := snap.Counters()
		assert.Contains(t, counters, "pfx.op.finish+repo=myrepo,result=success")
	})
}

func TestTagKey(t *testing.T) {
	tests := []struct {
		name string
		tags map[string]string
		want string
	}{
		{
			name: "empty",
			tags: map[string]string{},
			want: "",
		},
		{
			name: "single",
			tags: map[string]string{"repo": "go-code"},
			want: "repo:go-code",
		},
		{
			name: "sorted",
			tags: map[string]string{"result": "success", "repo": "go-code"},
			want: "repo:go-code,result:success",
		},
		{
			name: "three keys",
			tags: map[string]string{"z": "3", "a": "1", "m": "2"},
			want: "a:1,m:2,z:3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, TagKey(tt.tags))
		})
	}
}

