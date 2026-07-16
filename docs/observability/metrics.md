# observability/metrics

Tango's metrics library. A thin, concrete wrapper over `tally.Scope` that pins the metric path shape (`<scope>.<operation>.<metric>`) and enforces a uniform lifecycle convention: every instrumented operation emits a `start` counter and a `finish` duration histogram tagged with its outcome, so dashboards and alerts share a single query shape across all operations. It owns emission mechanics only — each domain package owns its own `Begin`/`Complete`, buckets, memoization, and any custom value histograms.

## What this package owns

- op-as-subscope emission: an instrument bound as `(op, name)` lands under `<scope>.<op>.<name>`
- a concrete, Tally-backed `Emitter` that binds instruments and copies tags before retaining them
- explicit no-op construction for deployments that intentionally do not emit
- a small set of shared vocabulary constants — operation names, tag keys, result values, and `Outcome(err)` classification — so every operation tags outcomes the same way

It does not own `Begin`/`Complete`, buckets, memoization, or custom value metrics. Those live in the domain package that implements each operation.

## Layout

```
observability/metrics/
├── emitter.go       concrete Tally-backed emitter
├── names.go         shared op / tag-key / result-value constants + Outcome()
└── emitter_test.go

controller/metrics.go           request lifecycle: Begin/Complete, repo memoization, buckets
graphrunner/metrics.go          graph runner lifecycle: Begin/Complete, buckets, target_counts
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

// Tagged returns a child emitter with additional fixed tags. It copies tags
// and does not modify its parent.
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

Only `New` returns an error, and only to surface a nil scope — a wiring mistake, not a runtime condition. The binding methods take constant `op`/`name` arguments and have no reachable failure path, so they return the instrument directly.

### Outcome

The metrics package exports `Outcome` so every domain classifies errors the same way. The `start`/`finish` lifecycle convention and its memoization are domain-local — see [Domain-local metrics](#domain-local-metrics).

```go
// Outcome maps an error to a result tag value.
func Outcome(err error) string {
    switch {
    case err == nil:
        return ResultSuccess
    case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
        return ResultCancelled
    default:
        return ResultFailure
    }
}
```

Every instrumented operation emits exactly two metrics under the `start`/`finish` convention:

| Metric | Kind | Records |
|---|---|---|
| `<scope>.<op>.start` | counter | operations attempted |
| `<scope>.<op>.finish` | histogram, `result`-tagged | operations completed — latency + outcome |

Custom value metrics like `target_counts` are separate — see [Domain-local metrics](#domain-local-metrics).

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
    ResultSuccess   = "success"
    ResultFailure   = "failure"
    ResultCancelled = "cancelled"
    ResultHit       = "hit"
    ResultMiss      = "miss"
)
```

The `result` tag is the sole outcome signal — there are no separate `succeeded`/`failed` counters. Success, failure, and cancelled counts are derived from the `finish` histogram by summing its buckets grouped by `result`.

## Dependency direction

The emitter is a fixed dependency for the lifetime of a component, so it lives on that component — passed to constructors and stored in a field.

```
controller workflow  -> controller metrics -> observability/metrics -> tally
domain operation     -> domain metrics     -> observability/metrics -> tally
infrastructure       -> local metrics      -> observability/metrics -> tally
```

## Domain-local metrics

Each domain implements `Begin`/`Complete` on its own metrics struct, which pre-builds result-tagged emitters at construction. Memoization is domain-local — each struct owns its cached emitters and their lifecycle.

```go
package graphrunner

// Buckets live at the callsite, visible without walking an object tree.
var (
    graphRunnerFinishBuckets = tally.MustMakeExponentialDurationBuckets(100*time.Millisecond, 2, 20) // 100ms .. ~1.7h
    targetCountBuckets       = tally.MustMakeExponentialValueBuckets(1, 10, 7)                       // 1 .. 1M
)

type graphRunnerOp struct {
    metrics *graphRunnerMetrics
    start   time.Time
}

type graphRunnerMetrics struct {
    byResult    map[string]*metrics.Emitter
    targetCount tally.Histogram
}

func newGraphRunnerMetrics(e *metrics.Emitter) *graphRunnerMetrics {
    // Memoize result-tagged emitters once at construction — Begin/Complete
    // reuse these instead of calling Tagged per request.
    return &graphRunnerMetrics{
        byResult: map[string]*metrics.Emitter{
            metrics.ResultSuccess:   e.Tagged(map[string]string{metrics.TagResult: metrics.ResultSuccess}),
            metrics.ResultFailure:   e.Tagged(map[string]string{metrics.TagResult: metrics.ResultFailure}),
            metrics.ResultCancelled: e.Tagged(map[string]string{metrics.TagResult: metrics.ResultCancelled}),
        },
        targetCount: e.ValueHistogram(metrics.OpGraphRunner, "target_counts", targetCountBuckets),
    }
}

func (m *graphRunnerMetrics) Begin() *graphRunnerOp {
    m.byResult[metrics.ResultSuccess].Counter(metrics.OpGraphRunner, "start").Inc(1)
    return &graphRunnerOp{metrics: m, start: time.Now()}
}

func (o *graphRunnerOp) Complete(err error) {
    o.metrics.byResult[metrics.Outcome(err)].
        DurationHistogram(metrics.OpGraphRunner, "finish", graphRunnerFinishBuckets).
        RecordDuration(time.Since(o.start))
}
```

The struct is built once in the constructor and stored on the runner:

```go
type nativeGraphRunner struct {
    metrics *graphRunnerMetrics
    // ... bazel, git, config ...
}

func NewNativeGraphRunner(p NativeGraphRunnerParams) GraphRunner {
    return &nativeGraphRunner{
        metrics: newGraphRunnerMetrics(p.Emitter),
        // ...
    }
}
```

At the callsite:

```go
func (g *nativeGraphRunner) Compute(ctx context.Context, ws workspace.Workspace) (_ targethasher.Result, retErr error) {
    op := g.metrics.Begin()
    defer func() { op.Complete(retErr) }()

    // ... bazel query, git file hashes, target hashing ...

    g.metrics.targetCount.RecordValue(float64(len(res.Targets)))
    return res, nil
}
```

## Request metrics

Request lifecycle follows the same `start`/`finish` convention, with the controller owning its own `Begin`/`Complete`. Outcomes are broken down by `repo`, so the struct memoizes result-tagged emitters per repo — `Tagged` is called once per distinct `(repo, result)` pair, not per request.

```go
package controller

// Buckets live at the callsite. Request-level latency is minutes-scale.
var getChangedTargetsFinishBuckets = tally.MustMakeExponentialDurationBuckets(
    time.Second, 3, 10, // 1s .. ~5h
)

type requestOp struct {
    byResult map[string]*metrics.Emitter
    op       string
    buckets  tally.DurationBuckets
    start    time.Time
}

type requestMetrics struct {
    base   *metrics.Emitter
    cached map[string]map[string]*metrics.Emitter // "repo:value" -> result emitters
}

func newRequestMetrics(e *metrics.Emitter) *requestMetrics {
    return &requestMetrics{base: e, cached: make(map[string]map[string]*metrics.Emitter)}
}

func (m *requestMetrics) resultEmittersFor(repo string) map[string]*metrics.Emitter {
    // Memoize per repo — Tagged is called once per distinct (repo, result)
    // pair, not per request.
    if re, ok := m.cached[repo]; ok {
        return re
    }
    tagged := m.base.Tagged(map[string]string{metrics.TagRepo: repo})
    re := map[string]*metrics.Emitter{
        metrics.ResultSuccess:   tagged.Tagged(map[string]string{metrics.TagResult: metrics.ResultSuccess}),
        metrics.ResultFailure:   tagged.Tagged(map[string]string{metrics.TagResult: metrics.ResultFailure}),
        metrics.ResultCancelled: tagged.Tagged(map[string]string{metrics.TagResult: metrics.ResultCancelled}),
    }
    m.cached[repo] = re
    return re
}

func (m *requestMetrics) Begin(repo, op string, buckets tally.DurationBuckets) *requestOp {
    re := m.resultEmittersFor(repo)
    re[metrics.ResultSuccess].Counter(op, "start").Inc(1)
    return &requestOp{byResult: re, op: op, buckets: buckets, start: time.Now()}
}

func (o *requestOp) Complete(err error) {
    o.byResult[metrics.Outcome(err)].
        DurationHistogram(o.op, "finish", o.buckets).
        RecordDuration(time.Since(o.start))
}
```

The controller builds `requestMetrics` once at construction:

```go
type controller struct {
    metrics *requestMetrics
    // ...
}

func newController(p Params) *controller {
    return &controller{
        metrics: newRequestMetrics(p.Emitter),
    }
}

func (c *controller) GetChangedTargets(req *pb.GetChangedTargetsRequest, stream pb.Tango_GetChangedTargetsServer) (retErr error) {
    repo := common.ToShortRemote(req.GetFirstRevision().GetRemote())
    op := c.metrics.Begin(repo, metrics.OpGetChangedTargets, getChangedTargetsFinishBuckets)
    defer func() { op.Complete(retErr) }()

    ctx, cancel := c.linkRequestCtx(stream.Context())
    defer cancel()
    // ... request workflow ...
    return nil
}
```

Sub-operations use the same struct with their own buckets:

```go
// Diffing is seconds-scale — different range from the enclosing operation.
var compareFinishBuckets = tally.MustMakeExponentialDurationBuckets(
    10*time.Millisecond, 2, 12, // 10ms .. ~40s
)

func (c *controller) compareTargetGraphs(repo string, ...) (retErr error) {
    op := c.metrics.Begin(repo, metrics.OpCompareTargetGraphs, compareFinishBuckets)
    defer func() { op.Complete(retErr) }()
    // ...
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

# custom value metric — target counts distribution
fetch service:tango name:controller.get_changed_targets.target_counts | histogram_percentile(95)
```

## Request-specific tags

Each distinct tag value is a new series, so tag values must be bounded — never request IDs, commit hashes, paths, or raw repo URLs. `repo` is safe only with an explicit cardinality budget and a normalized, allow-listed value; the handler above applies it that way (`ToShortRemote`), and tally's name+tags caching keeps each `(op, repo)` series bound once despite the per-request derivation.

## Buckets

Buckets are declared at each callsite and passed to `Begin` (for duration) or directly to `ValueHistogram` (for value metrics). There is no universal default — even within the same scope, buckets can be drastically different: the enclosing `get_target_graph` operation can last minutes, while its diffing sub-operation is merely seconds. A shared set is introduced only when operations intentionally share a semantic range.

Buckets are explicitly visible at the callsite: one click in the IDE to see what they are, without walking deeper into an object tree. Buckets are never promoted into `observability/metrics`.

## No-op behavior

Missing production wiring is an error, not a silent fallback:

```go
emitter, err := metrics.New(scope)
if err != nil {
    return nil, fmt.Errorf("create metrics emitter: %w", err)
}
```

Programs that intentionally emit nothing say so explicitly with `metrics.Nop()`. This keeps a forgotten wiring or a nil scope from quietly removing telemetry.