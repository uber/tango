# observability/metrics

Tango's metrics library. A thin, concrete wrapper over `tally.Scope` that pins the metric path shape (`<scope>.<operation>.<metric>`) and enforces a uniform lifecycle convention: every instrumented operation emits a `start` counter and a `finish` duration histogram tagged with its outcome, so dashboards and alerts share a single query shape across all operations. It owns emission mechanics plus the shared outcome vocabulary and latency/count bucket sets.

## What this package owns

- op-as-subscope emission: an instrument bound as `(op, name)` lands under `<scope>.<op>.<name>`
- a concrete, Tally-backed `Emitter` that binds instruments and delegates tagging to tally
- `Begin`/`Complete` lifecycle helpers that emit the `start`/`finish` pair with a consistent shape
- explicit no-op construction for deployments that intentionally do not emit
- a small set of shared vocabulary constants and the `Outcome` error classifier
- shared, cross-layer latency/count bucket sets so the same work is comparable across nesting layers

## Layout

```
observability/metrics/
├── emitter.go       concrete Tally-backed emitter + Begin / Complete helpers
├── buckets.go       shared, cross-layer latency / count bucket sets
├── names.go         shared op / tag-key / result-value constants + Outcome
└── emitter_test.go
```

Latency and count buckets come from a small set of shared, semantically-named sets (`FastDurationBuckets`, `SlowDurationBuckets`, `LargeCountBuckets`) in `buckets.go`; each callsite passes the one matching its timescale.

## Emitter

`Emitter` is a concrete struct: Tango has one metrics backend, and Tally already supplies test and no-op scopes. The API returns Tally instruments directly — it standardizes binding.

```go
package metrics

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
```

Every instrumented operation emits at least two metrics: `start`/`finish` convention and others

| Metric | Kind | Records |
|---|---|---|
| `<scope>.<op>.start` | counter | operations attempted |
| `<scope>.<op>.finish` | histogram, `result`-tagged | operations completed — latency |

Custom value metrics like `target_count` are recorded directly on the emitter alongside the pair — see [Usage](#usage).

## Lifecycle helpers

`Begin`/`Complete` emit the `start`/`finish` pair so callers don't repeat the counter-then-histogram boilerplate at every callsite. `Begin` takes the emitter the caller already holds — with the repo tag (and any other stable tags) baked in once at request entry — the operation name, and the histogram buckets. `Complete` tags the `finish` histogram with the outcome and records the elapsed duration.

```go
package metrics

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
```

The only tag reused across an operation's metrics is `repo`, so the caller bakes it into the emitter once and hands that emitter to `Begin` and to every custom metric. The operation name differs per metric (it becomes the subscope)

## Conventions

Metric and operation names are declared by the package that owns each operation, while the *outcome vocabulary* is shared: tag keys and result values live in `metrics/names.go` and every operation draws from them. Buckets are shared too — each callsite passes one of the `Fast`/`Slow`/`LargeCount` sets from `buckets.go` by timescale.

```go
package metrics

// Tag keys.
const (
    TagRepo   = "repo"
    TagResult = "result"
)

// Result values for TagResult on the finish histogram.
const (
    ResultSuccess = "success"
    ResultHit     = "hit"
    ResultMiss    = "miss"
)
```

### Outcome vocabulary

`Outcome(err)` maps an error to a `result` tag value for the `finish` histogram. A nil error is `success`; any non-nil error delegates to `tangoerrors.GetErrorCode(err).String()`, which classifies by `ErrorCode`:

| `result` value | When | Source |
|---|---|---|
| `success` | `err == nil` | hardcoded |
| `cancelled` | `errors.Is(err, context.Canceled)` | `ErrorCancelled.String()` |
| `user` | error wraps a `TangoError` with `ErrorUser` | `ErrorUser.String()` |
| `infra` | unclassified error or `ErrorInfra` | `ErrorInfra.String()` |
| `infra_retryable` | error wraps a `TangoError` with `ErrorInfraRetryable` | `ErrorInfraRetryable.String()` |

Note: `"failure"` is **not** a valid outcome value. Dashboards should filter on the concrete values above (`cancelled`, `user`, `infra`, `infra_retryable`) rather than a single `failure` bucket.

A `context.DeadlineExceeded` without a `TangoError` wrapper is classified as `infra` (a genuine timeout), not `cancelled` — only an explicit `context.Canceled` (client disconnect or shutdown) maps to `cancelled`.

Operation names are *not* centralized here — each consuming package declares its own op-name consts in its `metrics.go`, snake_cased after the interface method they measure (e.g. `get_target_graph`, `compute`, `lease`).

### Failure counters

In addition to the `finish` histogram, the controller emits a `<scope>.<op>.failures` counter on every error, tagged with `error_code` carrying the same `ErrorCode.String()` value (`cancelled`, `user`, `infra`, `infra_retryable`). This counter is emitted by `emitFailureMetric` in `controller/errors.go` and supplements the `result`-tagged histogram when per-error-code alerting is needed without histogram math.

### Cache-lookup counters

Three cache-lookup counters track per-layer hit rates under their parent RPC op:

| Counter name | Layer | Emitted by |
|---|---|---|
| `treehash_cache_lookup` | treehash-by-BuildDescription lookup | controller |
| `graph_cache_lookup` | graph-by-treehash download | controller + orchestrator |
| `compared_targets_cache_lookup` | compared-targets-by-treehash lookup | controller |

Each is a `result`-tagged counter (`hit` or `miss`) emitted via `RecordCacheLookup(e, parentOp, name, err)`. The semantics: a nil error is a `hit`, a `storage.NotFoundError` is a `miss`, and any other error (infra failure) emits **nothing** — an infra error is not a cache miss and must not skew the hit rate. Infra errors are already tracked by the `failures` counter.

The `result` tag on the `finish` histogram is the primary outcome signal. Success and error-class counts are derived from the `finish` histogram by summing its buckets grouped by `result`.

## Usage

Each component stores the `*metrics.Emitter` it was constructed with. At the top of an operation, the caller bakes in the `repo` tag once, calls `Begin`, and defers `Complete`.

```go
func (b *nativeOrchestrator) GetTargetGraph(ctx context.Context, req entity.GetTargetGraphRequest) (_ storage.GraphReader, retErr error) {
    e := b.emitter.Tagged(map[string]string{metrics.TagRepo: url.ToShortRemote(req.Build.Remote)})
    op := metrics.Begin(e, _opGetTargetGraph, metrics.SlowDurationBuckets)
    defer func() { op.Complete(retErr) }()

    // ... workspace lease, checkout, apply requests, compute graph ...
    return graphReader, nil
}
```

The controller follows the same shape; each RPC passes its own operation name and picks the bucket set matching the work's timescale (`Fast` vs `Slow`).

```go
func (c *controller) GetChangedTargets(req *pb.GetChangedTargetsRequest, stream pb.Tango_GetChangedTargetsServer) (retErr error) {
    e := c.emitter.Tagged(map[string]string{metrics.TagRepo: url.ToShortRemote(req.GetFirstRevision().GetRemote())})
    op := metrics.Begin(e, opGetChangedTargets, metrics.SlowDurationBuckets)
    defer func() { op.Complete(retErr) }()

    // custom value metric on the same repo-tagged emitter:
    e.ValueHistogram(opGetChangedTargets, "target_count", metrics.LargeCountBuckets).
        RecordValue(float64(changedCount))
    // ...
}
```

A sub-operation uses `Begin`/`Complete` for the `start`/`finish` duration exactly like the request handlers, reusing the repo-tagged emitter the caller already holds. Cache lookups within a sub-operation record a `RecordCacheLookup` counter alongside the duration.

```go
// In the orchestrator's GetTargetGraph, after computing the treehash:
cacheReadStart := time.Now()
graphReader, err := storage.NewGraphReader(ctx, b.storage, treehashPath)
recordStep(e, "cache_read_duration", cacheReadStart, metrics.FastDurationBuckets)
metrics.RecordCacheLookup(e, _opGetTargetGraph, metrics.GraphCacheLookup, err)
if err == nil {
    // cache hit — return early
    return graphReader, nil
}
```

### Querying

```
# operation rate
fetch service:tango name:controller.get_changed_targets.start

# success / failure / cancelled counts
fetch service:tango name:controller.get_changed_targets.finish | sum by (result)

# P95 latency of successful requests
fetch service:tango name:controller.get_changed_targets.finish result:success | histogram_percentile(95)

# scoped to a repo
fetch service:tango name:controller.get_changed_targets.finish result:success repo:my-monorepo | histogram_percentile(95)

# custom value metric — changed-target count distribution
fetch service:tango name:controller.get_changed_targets.target_count | histogram_percentile(95)
```

## Request-specific tags

Each distinct tag value is a new series, so tag values must be bounded — never request IDs, commit hashes, paths, or raw repo URLs. `repo` is safe only with an explicit cardinality budget and a normalized, allow-listed value; the handlers above apply it that way (`ToShortRemote`).

