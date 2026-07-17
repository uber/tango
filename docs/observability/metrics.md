# observability/metrics

Tango's metrics library. A thin, concrete wrapper over `tally.Scope` that pins the metric path shape (`<scope>.<operation>.<metric>`) and enforces a uniform lifecycle convention: every instrumented operation emits a `start` counter and a `finish` duration histogram tagged with its outcome, so dashboards and alerts share a single query shape across all operations. It owns emission mechanics only.

## What this package owns

- op-as-subscope emission: an instrument bound as `(op, name)` lands under `<scope>.<op>.<name>`
- a concrete, Tally-backed `Emitter` that binds instruments and delegates tagging to tally
- `Begin`/`Complete` lifecycle helpers that emit the `start`/`finish` pair with a consistent shape
- explicit no-op construction for deployments that intentionally do not emit
- a small set of shared vocabulary constants and the `Outcome` error classifier

## Layout

```
observability/metrics/
├── emitter.go       concrete Tally-backed emitter + Begin / Complete helpers
├── names.go         shared op / tag-key / result-value constants + Outcome
└── emitter_test.go
```

Bucket vars are declared as package-level values next to the handlers that use them.

## Emitter

`Emitter` is a concrete struct: Tango has one metrics backend, and Tally already supplies test and no-op scopes. The API returns Tally instruments directly — it standardizes binding.

```go
package metrics

// Emitter is a concrete, Tally-backed metrics emitter that pins the metric
// path shape to <scope>.<op>.<name>.
type Emitter struct {
    scope tally.Scope
}

// New creates an Emitter backed by the given tally.Scope. A nil scope is
// treated as a wiring error and returns an error.
func New(scope tally.Scope) (*Emitter, error) {
    if scope == nil {
        return nil, errors.New("metrics: nil scope")
    }
    return &Emitter{scope: scope}, nil
}

// Nop returns an Emitter that discards all metrics.
func Nop() *Emitter { return &Emitter{scope: tally.NoopScope} }

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

Metric names and buckets are declared by the package that owns each operation, but the *outcome vocabulary* is shared: operation names, tag keys, and result values live in `metrics/names.go` and every operation draws from them.

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
    TagRepo   = "repo"
    TagResult = "result"
)

// Result values for TagResult.
const (
    ResultSuccess   = "success"
    ResultFailure   = "failure"
    ResultCancelled = "cancelled"
    ResultHit       = "hit"
    ResultMiss      = "miss"
)
```
```go
// Outcome maps an error to a result tag value. Only an explicitly cancelled
// context (client disconnect or shutdown) is `cancelled`; a deadline exceeded
// is a genuine timeout and counts as `failure` (tagged infra on the
// failure_type axis).
func Outcome(err error) string {
    switch {
    case err == nil:
        return ResultSuccess
    case errors.Is(err, context.Canceled):
        return ResultCancelled
    default:
        return ResultFailure
    }
}
```
The `result` tag is the sole outcome signal. Success, failure, and cancelled counts are derived from the `finish` histogram by summing its buckets grouped by `result`.

## Usage

Each component stores the `*metrics.Emitter` it was constructed with. At the top of an operation, the caller bakes in the `repo` tag once, calls `Begin`, and defers `Complete`.

```go
func (b *nativeOrchestrator) GetTargetGraph(ctx context.Context, req entity.GetTargetGraphRequest) (_ storage.GraphReader, retErr error) {
    e := b.emitter.Tagged(map[string]string{metrics.TagRepo: common.ToShortRemote(req.Build.Remote)})
    op := metrics.Begin(e, metrics.OpGetTargetGraph, getTargetGraphBuckets)
    defer func() { op.Complete(retErr) }()

    // ... workspace lease, checkout, apply requests, compute graph ...
    return graphReader, nil
}
```

The controller follows the same shape; each RPC passes its own operation name and bucket range.

```go
func (c *controller) GetChangedTargets(req *pb.GetChangedTargetsRequest, stream pb.Tango_GetChangedTargetsServer) (retErr error) {
    e := c.emitter.Tagged(map[string]string{metrics.TagRepo: common.ToShortRemote(req.GetFirstRevision().GetRemote())})
    op := metrics.Begin(e, metrics.OpGetChangedTargets, getChangedTargetsBuckets)
    defer func() { op.Complete(retErr) }()

    // custom value metric on the same repo-tagged emitter:
    e.ValueHistogram(metrics.OpGetChangedTargets, "target_count", targetCountBuckets).
        RecordValue(float64(changedCount))
    // ...
}
```

A sub-operation uses `Begin`/`Complete` for the `start`/`finish` duration exactly like the request handlers, reusing the repo-tagged emitter the caller already holds.

```go
// opCacheRead is an extension op, declared next to the emit site.
const opCacheRead = "cache_read"

func (c *controller) readGraphCache(ctx context.Context, e *metrics.Emitter, key string) (_ storage.GraphReader, hit bool, retErr error) {
    op := metrics.Begin(e, opCacheRead, cacheReadBuckets)
    defer func() { op.Complete(retErr) }()

    return c.lookupGraph(ctx, key)
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

## Buckets

Buckets are declared at each callsite and passed to `Begin`. There is no universal default — even within the same scope, buckets can be drastically different: the enclosing `get_target_graph` operation can last minutes, while its diffing sub-operation is merely seconds. A shared set is introduced only when operations intentionally share a semantic range.

Buckets are explicitly visible at the callsite and are never promoted into `observability/metrics`.

## No-op behavior

Missing production wiring is an error, not a silent fallback:

```go
emitter, err := metrics.New(scope)
if err != nil {
    return nil, fmt.Errorf("create metrics emitter: %w", err)
}
```

Programs that intentionally emit nothing say so explicitly with `metrics.Nop()`. This keeps a forgotten wiring or a nil scope from quietly removing telemetry.
