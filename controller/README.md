# Controller

The controller is the request-handling layer of Tango behind the YARPC
handler. It turns each streaming RPC into a deterministic pipeline of cache
lookup, graph computation, comparison, and response streaming.

## Responsibilities

The package is intentionally thin. It owns the cross-cutting concerns that
sit between the wire protocol and the rest of the system:

- **Repository input.** Each call receives the resolved repository
  configuration from the handler. The controller does not look up the
  repository itself. The handler maps and validates each request and passes
  an entity request.
- **Read-through caching.** Where a request can be satisfied from previously
  computed artifacts, the controller fetches them from storage and streams
  them back without invoking the orchestrator. Cache misses fall through to
  computation, and computed results are written back asynchronously so they
  do not block the response.
- **Graph diffing.** Comparison-style RPCs fetch two target graphs
  concurrently, classify per-target changes (new, direct, indirect),
  optionally compute reverse-dependency distances, and assemble the response
  in a canonical ID space derived from per-request mappers.
- **Streaming and chunking.** GetChangedTargets emits its response as
  multiple stream messages sized to stay below the gRPC per-message limit.
  Targets, metadata, and topology deltas are chunked independently.
  GetTargetGraph returns a reader, and the handler sends each chunk.
- **Observability.** The controller emits per-phase timers and cache lookup
  counters, tagged with the repository. It also emits the send duration of
  GetChangedTargets. The handler owns the per-call counters and the
  classified failure metric of each RPC, and the send duration of
  GetTargetGraph.

## Collaborators

The controller composes other packages rather than reimplementing their
behavior:

- **Storage** — the controller reads cached treehashes, graphs, and
  comparison results, and writes computed comparison results back. It does
  not concern itself with the underlying medium.
- **Orchestrator** — invoked on cache misses to compute a target graph from
  a build description. The controller treats it as an opaque graph source.
- **Protobuf types** — the controller speaks the generated service and
  message types directly; it does not introduce its own domain model.

## Construction

The controller is built once at startup with its logger, storage,
orchestrator, optional metrics scope, optional max-message-bytes
configuration, and a graph config provider. The constructor
returns the `Controller` interface, which the `handler` package wraps to
expose the generated YARPC server surface.
