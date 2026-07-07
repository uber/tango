# observability/metrics

Tango's metrics library. A thin, opinionated wrapper over `tally.Scope` that pins the naming, and tag conventions Tango consumers emit so dashboards can query by tag rather than by name merging.

## Why

Tango is a library. Consumers wire it into their own `fx` graph with their own `tally.Scope`, and often deploy multiple flavors (long-running server, batch job, one-shot CLI) side-by-side. Without a shared convention every deployment invents its own metric names and tag layout, and dashboards devolve into per-name merges.

This package owns:

- op-as-subscope emission (`e.Inc("get_target_graph", "requests")` lands under `<scope>.get_target_graph.requests`)
- shared metric-name, op-name, and tag-key constants
- default histogram buckets sized for RPC latency and large target counts
- an in-flight gauge helper that owns the atomic counter callers otherwise reinvent
- a failure classifier that reuses the errors package
- context-based emitter propagation so downstream helpers do not need the emitter threaded through every signature

## Layout

```
observability/metrics/
├── emitter.go   — Emitter, TrackInFlight
├── options.go   — Option, WithTags, WithDurationBuckets, WithValueBuckets
├── buckets.go   — default histogram buckets
├── names.go     — Op*, metric name, Tag*, Result* constants
├── failure.go   — RecordFailure
└── context.go   — WithEmitter, FromContext
```

## Emitter

`Emitter` is an interface. `New(scope)` returns the default tally-backed implementation; `New(nil)` returns one backed by `tally.NoopScope` so callers do not need to special-case unwired metrics. This also serves as a noop fallback.

```go
type Emitter interface {
  Inc(op, name string, opts ...Option)
  Gauge(op, name string, v float64, opts ...Option)
  RecordDur(op, name string, d time.Duration, opts ...Option)
  RecordCount(op, name string, v int64, opts ...Option)
  Tagged(tags map[string]string) Emitter
}

type defaultEmitter struct {
  scope           tally.Scope
  baseTags        map[string]string
  durationBuckets tally.DurationBuckets
  valueBuckets    tally.ValueBuckets
}
```

`New(scope)` returns the default tally-backed implementation; `New(nil)` returns one backed by `tally.NoopScope` so callers do not need to special-case unwired metrics.

`Tagged` returns a child Emitter that adds tags to every subsequent emission. The parent is unchanged. The child shares the in-flight counter map, so a `TrackInFlight` increment on a parent and its decrement on a tagged child balance to zero.

Options are variadic and compose. `WithTags` merges into the base tag set with later-wins semantics on key collision. `WithDurationBuckets` / `WithValueBuckets` override the emitter's default histogram buckets on a single call.

## Op-as-subscope

Every emit call takes an op name as its first argument. `e.Inc("get_target_graph", "requests")` emits under `<scope>.get_target_graph.requests`. Consumers that own their own ops (an extension RPC, a background job) should declare `const opXYZ = "xyz"` next to the emit site rather than adding to `names.go`.

## TrackInFlight

Gauges are update-only, so an in-flight counter needs an owning atomic somewhere. Rather than force every call site to allocate its own `atomic.Int64` and remember to update the gauge on both the increment and the decrement, the Emitter owns a per-op `*int64` and exposes:

```go
defer c.emitter.TrackInFlight(metrics.OpGetTargetGraph)()
```

The counter map lives on the root Emitter, so a `Tagged` child shares the counter with its parent — a paired increment/decrement across a `Tagged` boundary always refers to the same gauge series. The emitted gauge carries `TagOperation=op` so a single time-series carries the count for every operation, split by op in the dashboard.

## RecordFailure

Every RPC calls one of:

- `e.Inc(op, Requests, WithTags({result: success}))` on the happy path
- `metrics.RecordFailure(e, op, err)` on the sad path

Both write to the same `<op>.requests` counter, distinguished only by tag. `RecordFailure` derives `failure_type` and `failure_source` from the error via the `observability/errors` package (separate design doc). Unclassified errors fall back to `unknown` / `infra`; `context.Canceled` / `context.DeadlineExceeded` shortcut to user-side types.

`RecordFailure` is a no-op on nil.

## Context propagation

Emitter is passed via contexts so downstream helpers don't need it threaded through every signature.

```go
type emitterKey struct{}
var noopEmitter = New(nil)

func WithEmitter(ctx context.Context, e *Emitter) context.Context {
   return context.WithValue(ctx, emitterKey{}, e)
}
func FromContext(ctx context.Context) *Emitter { // should never return nil
   e, ok := ctx.Value(emitterKey{}).(*Emitter)
   if !ok { return noopEmitter }
   return e
}
```

The RPC handler applies request-scope tags once and installs the tagged Emitter on the context. Helpers pull it back out with `FromContext`, which never returns nil — a missing key falls back to a package-level noop.

## Constants

All shared constants live in `names.go`: operation names (`OpGetTargetGraph`, `OpGetChangedTargets`, ...), metric names (`Requests`, `TotalDuration`, `InFlightRequests`, ...), tag keys (`TagRepo`, `TagEmitter`, `TagResult`, `TagFailureType`, `TagFailureSource`, ...), and `Result*` values (`ResultSuccess`, `ResultFail`, `ResultHit`, `ResultMiss`).

Failure tag values come from `observability/errors`. Packages that introduce a new failure source declare the constant next to the failure site.

## Usage

```go
func NewController(appCtx context.Context, p Params) pb.TangoYARPCServer {
    emitter := metrics.New(p.Scope).Tagged(map[string]string{
        metrics.TagEmitter: "server",
    })
    return &controller{emitter: emitter, /* ... */}
}

func (c *controller) GetChangedTargets(req *pb.GetChangedTargetsRequest, stream pb...) (retErr error) {
    defer c.emitter.TrackInFlight(metrics.OpGetChangedTargets)()
    e := c.emitter.Tagged(map[string]string{
        metrics.TagRepo: common.ToShortRemote(req.GetFirstRevision().GetRemote()),
    })
    ctx := metrics.WithEmitter(stream.Context(), e)
    start := time.Now()
    defer func() {
        e.RecordDur(metrics.OpGetChangedTargets, metrics.TotalDuration, time.Since(start))
        if retErr != nil {
            metrics.RecordFailure(e, metrics.OpGetChangedTargets, retErr)
        } else {
            e.Inc(metrics.OpGetChangedTargets, metrics.Requests,
                metrics.WithTags(map[string]string{metrics.TagResult: string(metrics.ResultSuccess)}))
        }
    }()
    // ... downstream helpers pull the tagged emitter via metrics.FromContext(ctx)
    return nil
}
```
