# observability/metrics

Tango's metrics library. A thin, concrete wrapper over `tally.Scope` that pins the metric path shape (`<scope>.<operation>.<metric>`) and enforces a uniform lifecycle convention: every instrumented operation emits a `start` counter and a `finish` duration histogram tagged with its outcome, so dashboards and alerts share a single query shape across all operations. It owns emission mechanics only.

## What this package owns

- op-as-subscope emission: an instrument bound as `(op, name)` lands under `<scope>.<op>.<name>`
- a concrete, Tally-backed `Emitter` that binds instruments and delegates tagging to tally
- explicit no-op construction for deployments that intentionally do not emit
- a small set of shared vocabulary constants

## Layout

```
observability/metrics/
├── emitter.go       concrete Tally-backed emitter
├── names.go         shared op / tag-key / result-value constants 
└── emitter_test.go

controller/metrics.go           request lifecycle: repo memoization, buckets
orchestrator/metrics.go         orchestrator lifecycle: repo memoization, buckets
```


## Emitter

`Emitter` is a concrete struct: Tango has one metrics backend, and Tally already supplies test and no-op scopes. The API returns Tally instruments directly — it standardizes binding.

```go
package metrics

type Emitter struct {
    scope tally.Scope
}

func New(scope tally.Scope) (*Emitter, error) {
    if scope == nil {
        return nil, errors.New("metrics: nil scope")
    }
    return &Emitter{scope: scope}, nil
}

func Nop() *Emitter { return &Emitter{scope: tally.NoopScope} }

func (e *Emitter) Tagged(tags map[string]string) *Emitter {
    if len(tags) == 0 {
        return e
    }
    return &Emitter{scope: e.scope.Tagged(tags)}
}

func (e *Emitter) Counter(op, name string) tally.Counter {
    return e.scope.SubScope(op).Counter(name)
}

func (e *Emitter) DurationHistogram(op, name string, b tally.DurationBuckets) tally.Histogram {
    return e.scope.SubScope(op).Histogram(name, b)
}

func (e *Emitter) ValueHistogram(op, name string, b tally.ValueBuckets) tally.Histogram {
    return e.scope.SubScope(op).Histogram(name, b)
}

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
```


Every instrumented operation emits exactly two metrics under the `start`/`finish` convention:

| Metric | Kind | Records |
|---|---|---|
| `<scope>.<op>.start` | counter | operations attempted |
| `<scope>.<op>.finish` | histogram, `result`-tagged | operations completed — latency + outcome |

Custom value metrics like `target_counts` are separate — see [Domain-local metrics](#domain-local-metrics).

## Conventions

Metric names and buckets are domain-local, but the *outcome vocabulary* is shared: operation names, tag keys, and result values live in `metrics/names.go` and every operation draws from them.

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
The `result` tag is the sole outcome signal. Success, failure, and cancelled counts are derived from the `finish` histogram by summing its buckets grouped by `result`.

## Dependency direction

The emitter is a fixed dependency for the lifetime of a component, so it lives on that component — passed to constructors and stored in a field.

```
controller workflow  -> controller metrics -> observability/metrics -> tally
domain operation     -> domain metrics     -> observability/metrics -> tally
infrastructure       -> local metrics      -> observability/metrics -> tally
```

## Domain-local metrics

Each domain implements its own metrics struct, which memoizes tagged emitters at construction or lazily per distinct tag value. Memoization is domain-local — each struct owns its cached emitters and their lifecycle.

```go
package orchestrator

var orchestratorFinishBuckets = tally.MustMakeExponentialDurationBuckets(time.Second, 2, 20) // 1s .. ~3h

type orchestratorOp struct {
    owner *orchestratorMetrics
    repo  string
    start time.Time
}

type orchestratorMetrics struct {
    base   *metrics.Emitter
    cached map[string]*metrics.Emitter
}

func newOrchestratorMetrics(e *metrics.Emitter) *orchestratorMetrics {
    return &orchestratorMetrics{base: e, cached: make(map[string]*metrics.Emitter)}
}

func (m *orchestratorMetrics) emitterFor(tags map[string]string) *metrics.Emitter {
    key := metrics.TagKey(tags)
    if e, ok := m.cached[key]; ok {
        return e
    }
    e := m.base.Tagged(tags)
    m.cached[key] = e
    return e
}

func (m *orchestratorMetrics) Begin(repo string) *orchestratorOp {
    m.emitterFor(map[string]string{metrics.TagRepo: repo}).
        Counter(metrics.OpGetTargetGraph, "start").Inc(1)
    return &orchestratorOp{owner: m, repo: repo, start: time.Now()}
}

func (o *orchestratorOp) Complete(err error) {
    o.owner.emitterFor(map[string]string{
        metrics.TagRepo:   o.repo,
        metrics.TagResult: metrics.Outcome(err),
    }).DurationHistogram(metrics.OpGetTargetGraph, "finish", orchestratorFinishBuckets).
        RecordDuration(time.Since(o.start))
}
```

The struct is built once in the constructor and stored on the orchestrator:

```go
type nativeOrchestrator struct {
    metrics *orchestratorMetrics
    // ... storage, repoManager, config ...
}

func NewNativeOrchestrator(appCtx context.Context, p Params) (Orchestrator, error) {
    return &nativeOrchestrator{
        metrics: newOrchestratorMetrics(p.Emitter),
        // ...
    }, nil
}
```

At the callsite:

```go
func (b *nativeOrchestrator) GetTargetGraph(ctx context.Context, req entity.GetTargetGraphRequest) (_ storage.GraphReader, retErr error) {
    repo := common.ToShortRemote(req.Build.Remote)
    op := b.metrics.Begin(repo)
    defer func() { op.Complete(retErr) }()

    // ... workspace lease, checkout, apply requests, compute graph ...
    return graphReader, nil
}
```

## Request metrics

The controller follows the same pattern with `Begin(repo, op, buckets)` so different RPC methods pass their own operation name and bucket range.

```go
package controller

var getChangedTargetsFinishBuckets = tally.MustMakeExponentialDurationBuckets(
    time.Second, 3, 10, // 1s .. ~5h
)

type requestOp struct {
    owner   *requestMetrics
    repo    string
    op      string
    buckets tally.DurationBuckets
    start   time.Time
}

type requestMetrics struct {
    base   *metrics.Emitter
    cached map[string]*metrics.Emitter
}

func newRequestMetrics(e *metrics.Emitter) *requestMetrics {
    return &requestMetrics{base: e, cached: make(map[string]*metrics.Emitter)}
}

func (m *requestMetrics) emitterFor(tags map[string]string) *metrics.Emitter {
    key := metrics.TagKey(tags)
    if e, ok := m.cached[key]; ok {
        return e
    }
    e := m.base.Tagged(tags)
    m.cached[key] = e
    return e
}

func (m *requestMetrics) Begin(repo, op string, buckets tally.DurationBuckets) *requestOp {
    m.emitterFor(map[string]string{metrics.TagRepo: repo}).
        Counter(op, "start").Inc(1)
    return &requestOp{owner: m, repo: repo, op: op, buckets: buckets, start: time.Now()}
}

func (o *requestOp) Complete(err error) {
    o.owner.emitterFor(map[string]string{
        metrics.TagRepo:   o.repo,
        metrics.TagResult: metrics.Outcome(err),
    }).DurationHistogram(o.op, "finish", o.buckets).
        RecordDuration(time.Since(o.start))
}
```

The controller builds `requestMetrics` once at construction:

```go
func (c *controller) GetChangedTargets(req *pb.GetChangedTargetsRequest, stream pb.Tango_GetChangedTargetsServer) (retErr error) {
    repo := common.ToShortRemote(req.GetFirstRevision().GetRemote())
    op := c.metrics.Begin(repo, metrics.OpGetChangedTargets, getChangedTargetsFinishBuckets)
    defer func() { op.Complete(retErr) }()
    // ...
}
```

Sub-operations use the same struct with their own buckets:

```go
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

Buckets are declared at each callsite and passed to callers. There is no universal default — even within the same scope, buckets can be drastically different: the enclosing `get_target_graph` operation can last minutes, while its diffing sub-operation is merely seconds. A shared set is introduced only when operations intentionally share a semantic range.

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