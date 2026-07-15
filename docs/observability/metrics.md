# observability/metrics

Tango's metrics library. A thin, concrete wrapper over `tally.Scope` that pins the metric path shape (`<scope>.<operation>.<metric>`). It owns emission mechanics only — each package owns the metric names, buckets, and instruments that describe its own behavior.

## What this package owns

- op-as-subscope emission: an instrument bound as `(op, name)` lands under `<scope>.<op>.<name>`
- a concrete, Tally-backed `Emitter` that binds instruments and copies tags before retaining them
- explicit no-op construction for deployments that intentionally do not emit

It does not own metric names or histogram buckets from callers, such as controller, Bazel, storage, graphrunner etc. Those live in the package that implements each operation.

## Layout

```
observability/metrics/
├── emitter.go       concrete Tally-backed emitter
└── emitter_test.go

controller/request_metrics.go   request lifecycle + buckets
core/bazel/metrics.go           bazel query metrics + buckets
graphrunner/metrics.go          graph runner metrics + buckets
```


## Emitter

`Emitter` is a concrete struct: Tango has one metrics backend, and Tally already supplies test and no-op scopes. The API returns Tally instruments directly — it standardizes binding.

```go
package metrics

type Emitter struct {
    scope tally.Scope
}

// New creates an emitter for scope. A nil scope is a configuration error.
func New(scope tally.Scope) (*Emitter, error)

// Nop creates an explicitly disabled emitter.
func Nop() *Emitter

// Tagged returns an emitter with additional fixed tags. It copies tags and
// does not modify its parent.
func (e *Emitter) Tagged(tags map[string]string) *Emitter

// Counter binds a counter under <scope>.<op>.<name>.
func (e *Emitter) Counter(op, name string) tally.Counter

// DurationHistogram binds a duration histogram using owner-selected buckets.
func (e *Emitter) DurationHistogram(op, name string, buckets tally.DurationBuckets) tally.Histogram

// ValueHistogram binds a value histogram using owner-selected buckets.
func (e *Emitter) ValueHistogram(op, name string, buckets tally.ValueBuckets) tally.Histogram
```

Only `New` returns an error, and only to surface a nil scope — a wiring mistake, not a runtime condition. The binding methods take constant `op`/`name` arguments and have no reachable failure path, so they return the instrument directly. Instruments are normally bound once during component construction and stored on a local metrics struct.

## Dependency direction

The emitter is a fixed dependency for the lifetime of a component, so it lives on that component — passed to constructors and stored in a field.

```
controller workflow  -> controller metrics -> observability/metrics -> tally
domain operation     -> domain metrics     -> observability/metrics -> tally
infrastructure       -> local metrics      -> observability/metrics -> tally
```

## Domain-local metrics

Each component defines a small metrics struct in its own vocabulary and binds its fixed instruments — and its bucket policy — once, at construction. Buckets are a default owned by the component, overrideable. For example,

```go
package bazel

const opQuery = "query"

// default bucket, chosen for bazel's own ranges
var (
    defaultQueryDurationBuckets = tally.MustMakeExponentialDurationBuckets(100*time.Millisecond, 3, 8) // 100ms .. ~3.6m
    defaultQueryTargetBuckets   = tally.MustMakeExponentialValueBuckets(1, 10, 6) // 1 .. 100k
)

type MetricParams struct {
    Emitter *metrics.Emitter
    QueryDurationBuckets tally.DurationBuckets
    QueryTargetBuckets   tally.ValueBuckets
}

type queryMetrics struct {
    succeeded   tally.Counter
    failed      tally.Counter
    duration    tally.Histogram
    targetCount tally.Histogram
}

func newQueryMetrics(p MetricParams) *queryMetrics {
    dur := p.QueryDurationBuckets
    if len(dur) == 0 {
        dur = defaultQueryDurationBuckets
    }
    tgt := p.QueryTargetBuckets
    if len(tgt) == 0 {
        tgt = defaultQueryTargetBuckets
    }

    e := p.Emitter
    return &queryMetrics{
        succeeded:   e.Counter(opQuery, "succeeded"),
        failed:      e.Counter(opQuery, "failed"),
        duration:    e.DurationHistogram(opQuery, "duration", dur),
        targetCount: e.ValueHistogram(opQuery, "target_count", tgt),
    }
}
```

The owning client takes `*queryMetrics` (or the `*metrics.Emitter` it builds one from) as a required dependency. Future changes to `bazel` metrics stay beside `bazel` behavior instead of in a cross-package inventory.

## Request metrics

Request outcome policy lives at the controller boundary. The pattern is controller-local, not part of the emitter package. Outcomes are broken down by `repo`, so a `requestMetrics` is built per request from a repo-tagged emitter; tally caches instruments by name+tags, so each `(op, repo)` series is bound once (lazily) and reused. Duration buckets follow the same default-and-override rule as domain-local metrics.

```go
// default request-latency buckets
var defaultRequestDurationBuckets = tally.MustMakeExponentialDurationBuckets(time.Millisecond, 3, 10) // 1ms .. ~59s

type requestMetrics struct {
    succeeded tally.Counter
    failed    tally.Counter
    duration  tally.Histogram
}

func newRequestMetrics(e *metrics.Emitter, op string, buckets tally.DurationBuckets) *requestMetrics {
    if len(buckets) == 0 {
        buckets = defaultRequestDurationBuckets
    }
    return &requestMetrics{
        succeeded: e.Counter(op, "succeeded"),
        failed:    e.Counter(op, "failed"),
        duration:  e.DurationHistogram(op, "duration", buckets),
    }
}

// Record records the request's duration and outcome. Total request rate is
// succeeded + failed.
func (m *requestMetrics) Record(start time.Time, err error) {
    m.duration.RecordDuration(time.Since(start))
    if err == nil {
        m.succeeded.Inc(1)
        return
    }
    m.failed.Inc(1)
}
```

The controller keeps the emitter and bucket policy, and builds `requestMetrics` per request off a repo-tagged emitter:

```go
const tagRepo = "repo"

func newController(p Params) *controller {
    return &controller{
        emitter:                p.Emitter,
        requestDurationBuckets: p.RequestDurationBuckets,
    }
}

func (c *controller) GetChangedTargets(req *pb.GetChangedTargetsRequest, stream pb.Tango_GetChangedTargetsServer) (retErr error) {
    // repo is bounded (allow-listed) and normalized to a stable key.
    e := c.emitter.Tagged(map[string]string{
        tagRepo: common.ToShortRemote(req.GetFirstRevision().GetRemote()),
    })
    m := newRequestMetrics(e, opGetChangedTargets, c.requestDurationBuckets)

    start := time.Now()
    defer func() { m.Record(start, retErr) }()

    ctx, cancel := c.linkRequestCtx(stream.Context())
    defer cancel()
    // ... request workflow ...
    return nil
}
```

## Request-specific tags

Each distinct tag value is a new series, so tag values must be bounded — never request IDs, commit hashes, paths, or raw repo URLs. `repo` is safe only with an explicit cardinality budget and a normalized, allow-listed value; the handler above applies it that way (`ToShortRemote`), and tally's name+tags caching keeps each `(op, repo)` series bound once despite the per-request derivation.

If the per-request emitter derivation ever shows up in a profile, memoize `*requestMetrics` in a map keyed by repo — but tally's caching makes that rarely worth it.

## Buckets

Buckets must be explicit. There is no universal default spanning RPCs, storage calls, Bazel queries, and multi-hour builds. The package that owns an operation selects its buckets from the expected distribution and its dashboard needs; a shared set is introduced only when operations intentionally share a semantic range.

The owning package defines a sensible default and keeps it local. Buckets are never promoted into `observability/metrics`

## No-op behavior

Missing production wiring is an error, not a silent fallback:

```go
emitter, err := metrics.New(scope)
if err != nil {
    return nil, fmt.Errorf("create metrics emitter: %w", err)
}
```

Programs that intentionally emit nothing say so explicitly with `metrics.Nop()`. This keeps a forgotten wiring or a nil scope from quietly removing telemetry.