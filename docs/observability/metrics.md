# observability/metrics

Tango's metrics library. A thin, concrete wrapper over `tally.Scope` that pins the metric path shape (`<scope>.<operation>.<metric>`). It owns emission mechanics only — each package owns the metric names, buckets, and instruments that describe its own behavior.

## What this package owns

- op-as-subscope emission: an instrument bound as `(op, name)` lands under `<scope>.<op>.<name>`
- a concrete, Tally-backed `Emitter` that binds instruments and copies tags before retaining them
- explicit no-op construction for deployments that intentionally do not emit

It does not own metric names from callers, such as controller, Bazel, storage, graph runner, or orchestrator, histogram buckets, error classification, or context propagation. Those live in the package that implements each operation.

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
controller workflow -> controller metrics -> observability/metrics -> tally
domain operation     -> domain metrics     -> observability/metrics -> tally
infrastructure       -> local metrics      -> observability/metrics -> tally
```

## Domain-local metrics

Each component defines a small metrics struct in its own vocabulary and binds its fixed instruments — and its bucket policy — once, at construction.

```go
package bazel

const opQuery = "query"

type queryMetrics struct {
    called      tally.Counter
    succeeded   tally.Counter
    failed      tally.Counter
    duration    tally.Histogram
    targetCount tally.Histogram
}

func newQueryMetrics(e *metrics.Emitter) *queryMetrics {
    return &queryMetrics{
        called:      e.Counter(opQuery, "called"),
        succeeded:   e.Counter(opQuery, "succeeded"),
        failed:      e.Counter(opQuery, "failed"),
        duration:    e.DurationHistogram(opQuery, "duration", queryDurationBuckets),
        targetCount: e.ValueHistogram(opQuery, "target_count", queryTargetBuckets),
    }
}
```

The owning client takes `*queryMetrics` (or the `*metrics.Emitter` it builds one from) as a required dependency. Future changes to Bazel metrics stay beside Bazel behavior instead of in a cross-package inventory.

## Request metrics

Request outcome policy lives at the controller boundary. The pattern is controller-local, not part of the emitter package.

```go
type requestMetrics struct {
    called    tally.Counter
    succeeded tally.Counter
    duration  tally.Histogram
    // ...
}

func (m *requestMetrics) Begin() *requestAttempt {
    m.called.Inc(1)
    return &requestAttempt{owner: m, start: time.Now()}
}

func (a *requestAttempt) Complete(err error) {
    a.once.Do(func() {
        m := a.owner
        m.duration.RecordDuration(time.Since(a.start))
        if err == nil {
            m.succeeded.Inc(1)
            return
        }
        m.failed.Inc(1)
        ...
    })
}
```

```go
func (c *controller) GetChangedTargets(req *pb.GetChangedTargetsRequest, stream pb.Tango_GetChangedTargetsServer) (retErr error) {
    attempt := c.getChangedTargetsMetrics.Begin()
    defer func() { attempt.Complete(retErr) }()

    ctx, cancel := c.linkRequestCtx(stream.Context())
    defer cancel()
    // ... request workflow ...
    return nil
}
```

## Request-specific tags

Tags with bounded cardinality may be applied to a derived emitter (`Tagged`) where the owning operation is constructed or recorded. Request identifiers, commit hashes, paths, and arbitrary repository URLs must never be tags. A repository tag is allowed only where a deployment has an explicit cardinality budget and normalizes the value consistently.

```go
const tagRepo = "repo"

func (c *controller) GetChangedTargets(req *pb.GetChangedTargetsRequest, stream pb.Tango_GetChangedTargetsServer) (retErr error) {
    e := c.emitter.Tagged(map[string]string{
        tagRepo: common.ToShortRemote(req.GetFirstRevision().GetRemote()),
    })

    e.Counter(opGetChangedTargets, "requests").Inc(1)
    // ...
}
```

## Buckets

Buckets must be explicit. There is no universal default spanning RPCs, storage calls, Bazel queries, and multi-hour builds. The package that owns an operation selects its buckets from the expected distribution and its dashboard needs; a shared set is introduced only when operations intentionally share a semantic range.

## No-op behavior

Missing production wiring is an error, not a silent fallback:

```go
emitter, err := metrics.New(scope)
if err != nil {
    return nil, fmt.Errorf("create metrics emitter: %w", err)
}
```

Programs that intentionally emit nothing say so explicitly with `metrics.Nop()`. This keeps a forgotten wiring or a nil scope from quietly removing telemetry.