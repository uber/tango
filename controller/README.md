# Controller

The controller is the request-handling layer of Tango. It implements the
generated YARPC service surface and turns each streaming RPC into a
deterministic pipeline of cache lookup, graph computation, comparison, and
response streaming.

## Responsibilities

The package is intentionally thin. It owns the cross-cutting concerns that
sit between the wire protocol and the rest of the system:

- **Request validation and translation.** Each RPC validates its inputs and
  normalizes them into the internal call shapes used downstream.
- **Read-through caching.** Where a request can be satisfied from previously
  computed artifacts, the controller fetches them from storage and streams
  them back without invoking the orchestrator. Cache misses fall through to
  computation, and computed results are written back asynchronously so they
  do not block the response.
- **Graph diffing.** Comparison-style RPCs fetch two target graphs
  concurrently, classify per-target changes (new, direct, indirect),
  optionally compute reverse-dependency distances, and assemble the response
  in a canonical ID space derived from per-request mappers.
- **Streaming and chunking.** Responses are emitted as multiple stream
  messages sized to stay below the gRPC per-message limit. Targets,
  metadata, and topology deltas are chunked independently.
- **Observability.** Every RPC emits per-call counters, per-phase timers,
  and a classified failure metric that distinguishes user from
  infrastructure errors.

## Collaborators

The controller composes other packages rather than reimplementing their
behavior:

- **Storage** — the controller reads cached treehashes, graphs, and
  comparison results, and writes computed comparison results back. It does
  not concern itself with the underlying medium.
- **Orchestrator** — invoked on cache misses to compute a target graph from
  a build description. The controller treats it as an opaque graph source.
- **Config** — supplies per-repository defaults (for example the BFS
  distance cap) and stream chunk sizes. Both are optional; sensible defaults
  apply when unset.
- **Protobuf types** — the controller speaks the generated service and
  message types directly; it does not introduce its own domain model.

## Construction

The controller is built once at startup with its logger, storage,
orchestrator, optional metrics scope, optional chunking configuration, and
an optional per-repository config provider. The constructor returns the
generated server interface, so the controller can be registered with a
YARPC dispatcher without additional adaptation.
