# observability/metrics

Tango's metrics library. A thin, opinionated wrapper over `tally.Scope` that pins the naming, and tag conventions Tango consumers emit so dashboards can query by tag rather than by name merging.

## Why

Tango is a library. Consumers wire it into their own `fx` graph with their own `tally.Scope`, and often deploy multiple flavors (long-running server, batch job, one-shot CLI) side-by-side. Without a shared convention every deployment invents its own metric names and tag layout, and dashboards devolve into per-name merges.

This package owns:

- op-as-subscope emission (`e.Inc("get_target_graph", "requests")` lands under `<scope>.get_target_graph.requests`)
- shared metric-name, op-name, and tag-key constants
- default histogram buckets sized for RPC latency and large target counts
- an in-flight gauge helper that owns the atomic counter callers otherwise reinvent
- context-based emitter propagation so downstream helpers do not need the emitter or scope threaded through every signature, thus context is required in functions that need to emit metrics

## Layout

```
observability/metrics/
├── emitter.go   — Emitter, TrackInFlight
├── options.go   — Option, WithTags, WithDurationBuckets, WithValueBuckets
├── buckets.go   — default histogram buckets
├── names.go     — Op*, metric name, Tag*, Result* constants
└── context.go   — WithEmitter, FromContext
```

## Emitter

`Emitter` is an interface. `New(scope)` returns the default tally-backed implementation; `New(nil)` returns a no-op, useful for tests.

```go
type Emitter interface {
  Inc(op, name string, opts ...Option)          // counter: +1 each call
  Gauge(op, name string, v float64, opts ...Option)
  RecordDur(op, name string, d time.Duration, opts ...Option)  // duration histogram
  RecordCount(op, name string, v int64, opts ...Option)        // value histogram (e.g. "4200 targets")
  Tagged(tags map[string]string) Emitter
}

type defaultEmitter struct {
  scope           tally.Scope
  durationBuckets tally.DurationBuckets
  valueBuckets    tally.ValueBuckets
}
```

```go
// New returns the default tally-backed Emitter. A nil scope falls back to
// tally.NoopScope
func New(scope tally.Scope) Emitter

// Tagged returns a child Emitter that adds tags to every subsequent emission.
// The parent is unchanged.
func (e *defaultEmitter) Tagged(tags map[string]string) Emitter

// WithTags attaches key/value tags to a single emission. Multiple WithTags
// compose with later-wins semantics on key collision.
func WithTags(tags map[string]string) Option

// WithDurationBuckets overrides the emitter's default duration histogram
// buckets for a single RecordDur call.
func WithDurationBuckets(b tally.DurationBuckets) Option

// WithValueBuckets overrides the emitter's default value histogram buckets
// for a single RecordCount call.
func WithValueBuckets(b tally.ValueBuckets) Option
```

## Op-as-subscope

Every emit call takes an op name as its first argument. For example, `e.Inc("get_target_graph", "requests")` emits under `<scope>.get_target_graph.requests`. Consumers that own their own ops (an extension RPC, a background job) should declare `const opXYZ = "xyz"` next to the emit site rather than adding to `names.go`.

## RecordRequest

Single helper for the RPC defer pattern — records duration and emits the appropriate success/failure counter:

```go
// RecordRequest records TotalDuration and emits a requests counter.
// If err is nil, tags result=success. Otherwise, derives failure_type
// and failure_source from the error via observability/errors.
func RecordRequest(e Emitter, op string, dur time.Duration, err error)
```

Internally:

```go
func RecordRequest(e Emitter, op string, dur time.Duration, err error) {
    e.RecordDur(op, TotalDuration, dur)
    if err != nil {
        recordFailure(e, op, err)
    } else {
        recordSuccess(e, op)
    }
}
```

Every RPC handler:

```go
defer func() {
    metrics.RecordRequest(e, metrics.OpGetChangedTargets, time.Since(start), retErr)
}()
```

`Inc` is the generic counter for everything else — cache lookups, patches applied, retries, workspace leases:

```go
e.Inc(op, TreehashCacheLookup, WithTags(map[string]string{TagResult: string(ResultHit)}))
e.Inc(op, Patch...)
```

## TrackInFlight

Gauges are update-only, so an in-flight counter needs an owning atomic somewhere. Rather than force every call site to allocate its own `atomic.Int64` and remember to update the gauge, the Emitter.TrackInFlight owns a per-op `*int64`:

```go
// TrackInFlight increments the in-flight counter for op, emits the gauge,
// and returns a decrement closure the caller is expected to defer.
// The counter is shared across Tagged children so increment/decrement
// always refers to the same gauge series.
func (e *defaultEmitter) TrackInFlight(op string) func()
```

Call site:

```go
defer c.emitter.TrackInFlight(metrics.OpGetTargetGraph)()
```

The emitted gauge carries `TagOperation=op` so a single time-series carries the count for every operation, split by op in the dashboard.

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

Observability is not business logic — a missing emitter should never crash the server or fail a request. In practice, a missing emitter means someone forgot to call `WithEmitter` in the handler setup; the consequence is lost metrics, which surfaces in dashboards and gets fixed. Callers who don't want metrics simply don't set an emitter rather than explicitly injecting a noop. The alternative — returning an error from `FromContext` — would force every call site to handle `if err := FromContext(ctx); err != nil {}` with nothing meaningful to do.

## Constants

### Operation names

| Constant | Value |
|---|---|
| `OpGetTargetGraph` | `get_target_graph` |
| `OpGetChangedTargets` | `get_changed_targets` |
| `OpGetChangedTargetGraph` | `get_changed_target_graph` |
| `OpGetGraph` | `get_graph` |
| `OpCompareTargetGraphs` | `compare_target_graphs` |
| `OpNativeOrchestrator` | `native_orchestrator` |
| `OpGraphRunner` | `graph_runner` |

### Metric names

| Constant | Value |
|---|---|
| `Requests` | `requests` |
| `TreehashCacheLookup` | `treehash_cache_lookup` |
| `GraphCacheLookup` | `graph_cache_lookup` |
| `ChangedTargetsCacheLookup` | `changed_targets_cache_lookup` |
| `GraphCacheFetchDuration` | `graph_cache_fetch_duration` |
| `ChangedTargetsCacheFetchDuration` | `changed_targets_cache_fetch_duration` |
| `CompareDuration` | `compare_duration` |
| `ChangedTargetsCount` | `changed_targets_count` |
| `TotalDuration` | `total_duration` |
| `Patch` | `patch` |
| `PatchDuration` | `patch_duration` |
| `BazelQueryDuration` | `bazel_query_duration` |
| `GitFileHashesDuration` | `git_file_hashes_duration` |
| `TargetHashDuration` | `target_hash_duration` |
| `TargetsCount` | `targets_count` |
| `StorageUploadDuration` | `storage_upload_duration` |
| `InFlightRequests` | `in_flight_requests` |
| `WorkspacesInFlight` | `in_flight_workspaces` |

### Tag keys

| Constant | Value |
|---|---|
| `TagRepo` | `repo` |
| `TagEmitter` | `emitter` |
| `TagResult` | `result` |
| `TagOperation` | `operation` |
| `TagFailureType` | `failure_type` |
| `TagFailureSource` | `failure_source` |

### Result values

| Constant | Value |
|---|---|
| `ResultSuccess` | `success` |
| `ResultFail` | `fail` |
| `ResultHit` | `hit` |
| `ResultMiss` | `miss` |

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
    // linkRequestCtx combines app context (shutdown) with stream context (client disconnect)
    ctx, cancel := c.linkRequestCtx(stream.Context())
    defer cancel()
    ctx = metrics.WithEmitter(ctx, e)
    start := time.Now()
    defer func() {
        metrics.RecordRequest(e, metrics.OpGetChangedTargets, time.Since(start), retErr)
    }()
    // ... downstream helpers pull the tagged emitter via metrics.FromContext(ctx)
    return nil
}
    return nil
}
```
