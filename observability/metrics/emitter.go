package metrics

import (
	"errors"
	"sort"
	"strings"

	"github.com/uber-go/tally"
)

// Emitter is a concrete, Tally-backed metrics emitter that pins the metric
// path shape to <scope>.<op>.<name>.
type Emitter struct {
	scope tally.Scope
}

// New creates an Emitter backed by the given tally.Scope.
// A nil scope is treated as a wiring error and returns an error.
func New(scope tally.Scope) (*Emitter, error) {
	if scope == nil {
		return nil, errors.New("metrics: nil scope")
	}
	return &Emitter{scope: scope}, nil
}

// Nop returns an Emitter that discards all metrics.
func Nop() *Emitter { return &Emitter{scope: tally.NoopScope} }

// Tagged returns a child Emitter whose instruments carry the given tags
// in addition to any tags already on the scope. It delegates to
// tally.Scope.Tagged directly.
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

// TagKey flattens a tag map into a stable, comparable string by sorting keys
// and joining as "k1:v1,k2:v2". It is exported so domain-local metrics
// structs can use it as a memoization cache key.
func TagKey(tags map[string]string) string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(tags[k])
	}
	return b.String()
}
