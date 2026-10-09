# Handler

The handler is Tango's YARPC transport layer. It implements the generated `pb.TangoYARPCServer` interface and turns each streaming RPC into request mapping, a call into `controller.Controller`, output filtering, and response streaming. It depends only on `controller.Controller` and never reaches into storage or the orchestrator directly.

## Responsibilities

- **RPC signatures and stream objects.** The handler owns every generated server method and the stream types used to send responses.
- **Request mapping and validation.** The handler converts each proto request to an entity request with `internal/mapper`. The mapper rejects invalid input, and the handler classifies that rejection as a user error before it calls the controller.
- **Proto and entity conversion.** All conversion between proto and entity types lives here. The controller never sees proto types.
- **Output filtering.** The handler applies distance filtering (`OutputConfig.max_distance`) and per-field stripping (hashes, tags, attributes) while it sends. It filters the full result of the controller chunk by chunk. One cached entry therefore serves every output option.
- **Response chunking.** The handler splits a changed-targets result into messages that stay within the gRPC per-message limit.
- **Observability.** Each RPC emits its own lifecycle counters and a classified failure metric under the `handler` metrics scope, tagged with the repository. A rejected request carries the repository tag `unknown`. The controller keeps its own phase metrics.
- **Wire error conversion.** The handler converts the final error of each RPC into a YARPC error with a `TangoError` detail, so clients see a classified error code on the wire.
- **Not-yet-implemented RPCs.** `GetChangedTargetGraph` is not implemented. It returns a YARPC Unimplemented error.

## Collaborators

- **`controller.Controller`** is the only business dependency. The handler calls `GetTargetGraph` and `GetChangedTargets` with entity requests and the resolved repository configuration, and streams the full result back.
- **`config.RepositoryConfigProvider`** resolves the repository of each request. The handler rejects an unconfigured remote as a user error before it calls the controller, tags its metrics with the repository ID, and passes the resolved repository configuration to the controller.
- **Protobuf types.** The handler is the only layer that speaks the generated service and message types.

## Construction

The handler is built once at startup from a `Params` value: a logger, a `controller.Controller`, a repository config provider, an optional metrics scope, and an optional maximum message size. `New` returns the generated server interface, so a YARPC dispatcher can register the handler without adaptation.
