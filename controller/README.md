# Controller

The controller is Tango's entity-level business logic layer. It implements
the `Controller` interface that the `handler` package depends on, and turns
each call into a deterministic pipeline of cache lookup, graph computation,
and comparison. It speaks only entity types; it never imports the generated
proto package or `internal/mapper`'s proto conversions.

## Responsibilities

The package is intentionally thin. It owns the cross-cutting concerns that
sit between the handler and the rest of the system:

- **Repository input.** Each call receives the resolved repository
  configuration from the handler. The controller does not look up the
  repository itself.
- **Read-through caching.** Where a request can be satisfied from previously
  computed artifacts, the controller fetches them from storage and returns
  them without invoking the orchestrator. Cache misses fall through to
  computation, and computed results are written back asynchronously so they
  do not block the caller.
- **Graph diffing.** `GetChangedTargets` fetches two target graphs
  concurrently, classifies per-target changes (new, direct, indirect),
  optionally computes reverse-dependency distances, and assembles the result
  in a canonical ID space derived from per-request mappers.
- **Full, unfiltered results.** The controller always returns the complete
  computed or cached payload. Distance filtering and field stripping based on
  output options are the handler's responsibility, applied while sending, so
  a single cached entry serves every output-option combination.
- **Observability.** The controller emits per-phase timers and cache lookup
  counters, tagged with the repository. The handler owns the per-RPC
  lifecycle counters and the classified failure metric.

## Collaborators

The controller composes other packages rather than reimplementing their
behavior:

- **Storage** — the controller reads cached treehashes, graphs, and
  comparison results, and writes computed comparison results back. It does
  not concern itself with the underlying medium.
- **Orchestrator** — invoked on cache misses to compute a target graph from
  a build description. The controller treats it as an opaque graph source.
- **Entity types** - the controller speaks `entity` request and response
  types directly; it does not introduce its own wire format and never touches
  the generated proto types.

## Construction

The controller is built once at startup with its logger, storage,
orchestrator, optional metrics scope, optional max-message-bytes
configuration, and repository/graph config providers. `NewController` returns
the `Controller` interface, which the `handler` package wraps to expose the
generated YARPC server surface.
