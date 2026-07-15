# observability/metrics

Tango's metrics library. A thin, concrete wrapper over `tally.Scope` that pins the metric path shape (`<scope>.<operation>.<metric>`). It owns emission mechanics only — each package owns the metric names, buckets, and instruments that describe its own behavior.

## What this package owns

- op-as-subscope emission: an instrument bound as `(op, name)` lands under `<scope>.<op>.<name>`
- a concrete, Tally-backed `Emitter` that binds instruments and copies tags before retaining them
- explicit no-op construction for deployments that intentionally do not emit
- a small set of shared vocabulary constants — operation names, tag keys, and result values — so every operation tags outcomes the same way

It does not own metric names or histogram buckets from callers, such as controller, Bazel, storage, graphrunner etc. Those live in the package that implements each operation.

## Layout

```
observability/metrics/
├── emitter.go       concrete Tally-backed emitter
├── names.go         shared op / tag-key / result-value constants
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
    scope    tally.Scope
    baseTags map[string]string
}

// New creates an emitter for scope. A nil scope is a configuration error;
// callers that intentionally emit nothing use Nop instead.
func New(scope tally.Scope) (*Emitter, error) {
    if scope == nil {
        return nil, errors.New("metrics: nil scope")
    }
    return &Emitter{scope: scope}, nil
}

// Nop returns an explicitly disabled emitter backed by tally.NoopScope.
func Nop() *Emitter { return &Emitter{scope: tally.NoopScope} }

// Tagged returns a child that merges tags into every instrument it binds. It
// copies tags and does not modify its parent.
func (e *Emitter) Tagged(tags map[string]string) *Emitter {
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
    return &Emitter{scope: e.scope, baseTags: merged}
}

// Counter binds a counter under <scope>.<op>.<name>.
func (e *Emitter) Counter(op, name string) tally.Counter {
    return e.opScope(op).Counter(name)
}

// DurationHistogram binds a duration histogram using owner-selected buckets.
func (e *Emitter) DurationHistogram(op, name string, b tally.DurationBuckets) tally.Histogram {
    return e.opScope(op).Histogram(name, b)
}

// ValueHistogram binds a value histogram using owner-selected buckets.
func (e *Emitter) ValueHistogram(op, name string, b tally.ValueBuckets) tally.Histogram {
    return e.opScope(op).Histogram(name, b)
}

// opScope applies the op subscope and any accumulated tags.
func (e *Emitter) opScope(op string) tally.Scope {
    s := e.scope.SubScope(op)
    if len(e.baseTags) > 0 {
        s = s.Tagged(e.baseTags)
    }
    return s
}
```

Only `New` returns an error, and only to surface a nil scope — a wiring mistake, not a runtime condition. The binding methods take constant `op`/`name` arguments and have no reachable failure path, so they return the instrument directly. Instruments are usually bound once during component construction and stored on a local metrics struct; a component whose metrics carry a per-request tag (see [Request metrics](#request-metrics)) builds its struct per request instead.

## Conventions

Metric names and buckets are domain-local, but the *outcome vocabulary* is shared: an operation that tags `result=success` and one that tags `result=succeeded` will not aggregate together, and a dashboard can't discover that mismatch. So operation names, tag keys, and result values live in `metrics/names.go` and every operation draws from them.

```go
package metrics

// Operation names — the <op> subscope for Tango's core operations. Extension
// operations declare their own const next to the emit site.
const (
    OpGetTargetGraph        = "get_target_graph"
    OpGetChangedTargets     = "get_changed_targets"
    OpGetChangedTargetGraph = "get_changed_target_graph"
    OpGetGraph              = "get_graph"
    OpCompareTargetGraphs   = "compare_target_graphs"
    OpGraphRunner           = "graph_runner"
)

// Tag keys.
const (
    TagRepo      = "repo"
    TagResult    = "result"
    TagOperation = "operation"
)

// Result values for TagResult.
const (
    ResultSuccess = "success"
    ResultFailure = "failure"
    ResultHit     = "hit"
    ResultMiss    = "miss"
)
```

An outcome is recorded as one latency histogram tagged `result=success|failure` — not a pair of `succeeded`/`failed` counters. A histogram already carries a per-bucket count, so the success and failure *rates* are derived from it (sum the buckets, group by `result`), and a separate outcome counter would be redundant. See [Request metrics](#request-metrics).

## Dependency direction

The emitter is a fixed dependency for the lifetime of a component, so it lives on that component — passed to constructors and stored in a field.

```
controller workflow  -> controller metrics -> observability/metrics -> tally
domain operation     -> domain metrics     -> observability/metrics -> tally
infrastructure       -> local metrics      -> observability/metrics -> tally
```

## Domain-local metrics

A domain component defines a small metrics struct in its own vocabulary. Instruments whose tags are fixed for the component's lifetime are bound once, at construction; an instrument whose tags depend on the outcome (the `result`-tagged latency histogram) is bound at record time — tally caches it by name+tags, and the `result` value is bounded (`success`/`failure`), so this stays low-cardinality. Buckets are a default owned by the component, overrideable. For example,

```go
package bazel

const opQuery = "query"

// default buckets, chosen for bazel's own ranges
var (
    defaultQueryDurationBuckets = tally.MustMakeExponentialDurationBuckets(100*time.Millisecond, 3, 8) // 100ms .. ~3.6m
    defaultQueryTargetBuckets   = tally.MustMakeExponentialValueBuckets(1, 10, 6)                       // 1 .. 100k
)

type queryMetrics struct {
    emitter     *metrics.Emitter
    buckets     tally.DurationBuckets
    targetCount tally.Histogram
}

func newQueryMetrics(e *metrics.Emitter, dur tally.DurationBuckets, tgt tally.ValueBuckets) *queryMetrics {
    if len(dur) == 0 {
        dur = defaultQueryDurationBuckets
    }
    if len(tgt) == 0 {
        tgt = defaultQueryTargetBuckets
    }
    return &queryMetrics{
        emitter:     e,
        buckets:     dur,
        targetCount: e.ValueHistogram(opQuery, "target_count", tgt),
    }
}

// record emits query latency tagged with the outcome (success/failure counts
// are derived from this histogram) and the target count on success.
func (m *queryMetrics) record(start time.Time, targets int, err error) {
    result := metrics.ResultSuccess
    if err != nil {
        result = metrics.ResultFailure
    }
    m.emitter.Tagged(map[string]string{metrics.TagResult: result}).
        DurationHistogram(opQuery, "duration", m.buckets).
        RecordDuration(time.Since(start))
    if err == nil {
        m.targetCount.RecordValue(float64(targets))
    }
}
```

The bazel client's exported `Params` carries the `Emitter` and optional bucket overrides and passes them to `newQueryMetrics`; unset buckets fall back to the defaults above. Keeping this here means future changes to `bazel` metrics stay beside `bazel` behavior instead of in a cross-package inventory.

## Request metrics

Request outcome policy lives at the controller boundary — the pattern is controller-local, not part of the emitter package. A request emits two things: a `started` counter when it begins, and a single latency histogram tagged with the outcome when it completes. The success/failure *count* is derived from the histogram (sum its buckets, group by `result`), so there is no separate outcome counter. Outcomes are broken down by `repo`, so `requestMetrics` is built per request from a repo-tagged emitter; tally caches by name+tags, so each `(op, repo)` series is bound once and reused. Duration buckets follow the same default-and-override rule as domain-local metrics.

```go
// default request-latency buckets
var defaultRequestDurationBuckets = tally.MustMakeExponentialDurationBuckets(time.Millisecond, 3, 10) // 1ms .. ~59s

type requestMetrics struct {
    emitter *metrics.Emitter
    op      string
    buckets tally.DurationBuckets
    started tally.Counter
}

func newRequestMetrics(e *metrics.Emitter, op string, buckets tally.DurationBuckets) *requestMetrics {
    if len(buckets) == 0 {
        buckets = defaultRequestDurationBuckets
    }
    return &requestMetrics{emitter: e, op: op, buckets: buckets, started: e.Counter(op, "started")}
}

// requestAttempt is one in-progress request.
type requestAttempt struct {
    owner *requestMetrics
    start time.Time
}

// Begin records that the request started and returns the attempt to Complete.
func (m *requestMetrics) Begin() *requestAttempt {
    m.started.Inc(1)
    return &requestAttempt{owner: m, start: time.Now()}
}

// Complete records latency into a single histogram tagged with the outcome;
// success/failure counts are derived from it.
func (a *requestAttempt) Complete(err error) {
    result := metrics.ResultSuccess
    if err != nil {
        result = metrics.ResultFailure
    }
    a.owner.emitter.Tagged(map[string]string{metrics.TagResult: result}).
        DurationHistogram(a.owner.op, "latency", a.owner.buckets).
        RecordDuration(time.Since(a.start))
}
```

The handler begins the attempt off a repo-tagged emitter and completes it in a defer:

```go
func newController(p Params) *controller {
    return &controller{
        emitter:                p.Emitter,
        requestDurationBuckets: p.RequestDurationBuckets,
    }
}

func (c *controller) GetChangedTargets(req *pb.GetChangedTargetsRequest, stream pb.Tango_GetChangedTargetsServer) (retErr error) {
    // repo is bounded (allow-listed) and normalized to a stable key.
    e := c.emitter.Tagged(map[string]string{
        metrics.TagRepo: common.ToShortRemote(req.GetFirstRevision().GetRemote()),
    })
    attempt := newRequestMetrics(e, metrics.OpGetChangedTargets, c.requestDurationBuckets).Begin()
    defer func() { attempt.Complete(retErr) }()

    ctx, cancel := c.linkRequestCtx(stream.Context())
    defer cancel()
    // ... request workflow ...
    return nil
}
```

## Request-specific tags

Each distinct tag value is a new series, so tag values must be bounded — never request IDs, commit hashes, paths, or raw repo URLs. `repo` is safe only with an explicit cardinality budget and a normalized, allow-listed value; the handler above applies it that way (`ToShortRemote`), and tally's name+tags caching keeps each `(op, repo)` series bound once despite the per-request derivation.

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