# Tango (Target Analyzer) Repository Guide

## Key Concepts

Tango (**Ta**rget A**n**alyzer in **Go**) is a standalone library and service that fetches and compares Bazel target graphs across revisions of a repository. It answers two related questions:

1. **What does the target graph look like at a given revision?**
2. **Which targets changed between two revisions, and what is each changed target's BFS distance from the nearest direct cause?**

It is designed to run independently of the monorepo it analyzes — the only inputs are a remote URL, a base SHA, and optionally a set of change requests (PR/diff URLs + commit SHAs) to layer on top.

### Core design properties

1. **Content identity and deterministic computation** — the materialized git treehash identifies source content, while cache keys also include the computation-affecting inputs represented by each artifact's key schema. Canonical change URIs pin change requests to exact commits. Any new input that can change computed output requires a cache-key review; output-only options stay outside cache keys and are applied while sending.
2. **Value-oriented identities and safe handoffs** — repository identities and computed results should be treated as immutable after they are handed to another layer or goroutine. Tango still uses mutable workspaces, graph builders, slices, and maps internally; copy mutable collections before normalization or concurrent use when the caller may retain them.
3. **Scoped eventual consistency** — eventual consistency applies to the asynchronous, best-effort compared-target cache. Graph and treehash writes that occur while storing a computed graph are synchronous and request-bound, so they must succeed before that graph is returned.
4. **Streaming, chunked responses** — target graphs and change results are split into chunks to stay within gRPC per-message limits. Metadata mappings (target IDs → names, rule types, tags, attributes) may also span multiple chunks; consumers must merge them before use.
5. **ID-mapped payloads** — over the wire, targets reference each other (and their rule types, tags, attributes) by `int32` IDs into per-stream metadata maps. Comparison code re-maps both inputs into a canonical per-call namespace and prunes unreferenced metadata entries before sending. IDs are not guaranteed to be consistent across multiple target graphs.
6. **Always-on cancellation** — both request-bound and application-bound cancellation signals are honored. Long-running loops whose iteration count can be large check `ctx.Err()` periodically. A client disconnect cancels request work, while application shutdown cancels both request work and application-lifetime background work.

## Architecture

### Project Layout

```
tango/                              # repo root (Go module github.com/uber/tango)
├── proto/                          # Proto definitions (.proto files)
├── tangopb/                        # Generated proto code (committed)
│   └── tangopbmock/                # Generated mocks for YARPC server interfaces
├── controller/                     # YARPC service implementation (business logic, transport-adjacent)
├── orchestrator/                   # Cross-component coordinator: workspace lease, checkout, graph compute, cache I/O
│   └── orchestratormock/
├── graphrunner/                    # Strategy-pluggable target-graph computation
│   └── mock/
├── entity/                         # Domain value types (BuildDescription, ChangedTargets, TargetGraph, etc.)
├── mapper/                         # Proto-to-entity and entity-to-proto conversion
├── internal/                       # Internal-only packages (not importable by external consumers)
│   ├── mapper/                     # Internal mapping helpers
│   ├── streaming/                  # Chunked stream assembly
│   ├── targetdiff/                 # Per-target change classification
│   └── url/                        # URL normalization and hashing
├── config/                         # YAML config parsing and validation (storage, service, repository)
├── core/                           # Reusable infrastructure with no domain dependencies
│   ├── git/                        # Git CLI wrapper (clone, fetch, checkout, rev-parse, ...)
│   ├── repomanager/                # Per-repo worker-pool / clone manager
│   ├── storage/                    # Blob storage interface and impls (in-memory, disk)
│   ├── workspace/                  # Workspace abstraction over a git checkout, request application
│   ├── cachekey/                   # Cache path/key construction for graphs, treehashes, compared-target results
│   ├── errors/                     # TangoError type and ErrorCode classification
│   └── ...                         # bazel, execcmd, itg, targethasher, ...
├── observability/                  # Cross-cutting observability
│   └── metrics/                    # Tally-backed metrics emitter, Begin/Complete lifecycle, bucket definitions
├── integration/                    # Integration tests and benchmarks (requires a local repo)
├── docs/                           # Supplementary documentation (error taxonomy, observability notes)
├── example/                        # Runnable server + client and benchmark CLI
│   ├── client/
│   └── cmd/query-bench/
└── tools/                          # Bazelisk wrapper and tooling
```

The top-level split is by **responsibility**, not by domain: `controller/` handles the RPC surface, `orchestrator/` drives workspace materialization and graph production, `graphrunner/` computes a graph from a materialized workspace, and `core/` holds reusable infrastructure. Interfaces are used at behavioral extension seams so implementations can be mocked or replaced without forcing every cross-layer dependency into an interface.

### Controllers

The controller is the YARPC service implementation. It owns transport-adjacent concerns: request validation, metrics, cancellation linkage, response streaming, output filtering, comparison fan-out, and compared-target caching. It does **not** own workspace creation, git operations, or graph computation.

Every RPC records operation metrics, classifies invalid input as a user error, preserves error chains, attempts applicable cache reads before expensive work, delegates graph production to the orchestrator, and converts the final error at the wire boundary.

### Orchestrator

The orchestrator provides the target graph, typically by running Bazel. An implementation may perform the work on the local host or delegate it to another stateful subsystem, such as CI infrastructure, that manages checkout and computation.

### Graphrunner

`graphrunner.GraphRunner` computes a target graph from an already-materialized `workspace.Workspace`. The orchestrator resolves repository identity and owns cache behavior; the graph runner turns the resulting source tree into a `targethasher.Result` without knowing about storage, transport, or the triggering request.

Keep the contract narrow and strategy-agnostic. New computation implementations satisfy the same interface and receive all required dependencies through construction rather than reaching into controller or storage concerns.

### Entity guidelines

Entities are Tango's request, result, and storage data models; they are not persistent versioned aggregates.

1. **Describe data, not choreography** — comments state what a type or field means, its units, optionality, identity, and invariants. Component ownership and write paths belong in the architecture sections.
2. **Prefer values for identities and configuration** — use value structs for `BuildDescription`, requests, configs, and constructor params. Pointers are appropriate for optional payloads, mutation, or shared ownership.
3. **Keep wire conversion at the mapper boundary** — protobuf validation and proto/entity conversion belong in `internal/mapper` or `mapper`, not in the orchestrator or graph algorithms.

### Extensions and interfaces

Behavioral interfaces live with the capability they describe and have a single responsibility:

- `core/storage.Storage` — blob get/put/exists/list keyed by string
- `core/git.Interface` — git CLI operations
- `core/repomanager.RepoManager` — workspace lease/release
- `core/workspace.Workspace` + `core/workspace.Request` — checkout, apply
- `graphrunner.GraphRunner` — compute a graph from a workspace
- `orchestrator.Orchestrator` — top-level entry point

**Design interfaces for the technology *space*, not the implementation in front of you.** The contract must be cheaply satisfiable by every plausible backend, not just the one being built today. For example, the `Storage` interface offers `Get`/`Put`/`Exists`/`List` keyed by a string — primitives that a disk, an in-memory map, S3, GCS, or a CDN can all satisfy without contortion.

Common over-constraints to avoid:
- **Server-side filters or queries** — push filtering and aggregation to the caller; keep storage responsible only for get/put-by-key semantics.
- **Batch atomicity** — many backends cannot provide multi-blob transactions. Prefer single-blob primitives, caller loops, and deterministic content identities.
- **Strict ordering or exactly-once background work** — make cache writes retry-safe and deterministic instead of depending on execution order.
- **Assumed-local latency** — design remote-capable operations for cancellation, retry classification, and backend-owned timeouts. The controller must not encode one backend's I/O budget.

Concrete backend construction and deployment-level routing belong in the composition root, such as `example/main.go`, which has the configuration and lifecycle context needed to choose implementations. Per-request computation selection remains orchestrator behavior because the requested strategy is part of `BuildDescription`.

When in doubt, ask: *"If the next implementation were S3, GCS, a remote RPC service, or an in-memory map, could it satisfy this signature without contortion?"* If the answer is no, simplify the contract.

### Import Paths

Paths follow the directory layout under `github.com/uber/tango/`:

- Service: `github.com/uber/tango/controller`, `.../orchestrator`, `.../graphrunner`
- Proto (generated): `github.com/uber/tango/tangopb`
- Generated mocks: `github.com/uber/tango/{package}/mock` or `.../{package}mock` (see Naming Conventions)
- Config: `github.com/uber/tango/config`
- Reusable infra: `github.com/uber/tango/core/{pkg}` (e.g. `.../core/storage`, `.../core/git`, `.../core/repomanager`)

## Development

### Build System

Bazel with Bzlmod (NOT WORKSPACE).

- **Dependencies**: `MODULE.bazel` + `go.mod` — both must be updated. Add direct Go dependencies explicitly to `MODULE.bazel`.
- **Bazel wrapper**: `./tools/bazel` (Bazelisk). With direnv (`.envrc`), use `bazel` directly.
- **BUILD files**: Every Go package needs `BUILD.bazel`. Run `make gazelle` after adding/removing Go files or imports.
- **CI enforces** BUILD files are in sync — always run `make gazelle` before committing.
- After adding an external dependency, run `bazel mod tidy` to register it.

### Proto Generation

Generated proto files are committed to the repo. When modifying `proto/tango.proto`:

1. Edit `proto/tango.proto`.
2. `make proto` (generates gogoslick, gRPC, and YARPC bindings under `tangopb/`).
3. Commit all generated files.
4. Regenerate any mocks that depend on the changed interfaces (`mockgen`), then `make gazelle`.

`make clean-proto && make proto` regenerates from scratch — only needed when in doubt about the state of generated files.

### Mocks

Mocks use `go.uber.org/mock` (mockgen) and are checked in. The conventions:

- **Subdirectory under the package being mocked**: `core/git/gitmock/`, `core/storage/storagemock/`, `core/repomanager/mock/`, `graphrunner/mock/`, `orchestrator/orchestratormock/`. Either `{pkg}mock` or `mock` is used — match whatever already exists in the parent package.
- **Package-level mocks** for cross-cutting interfaces (e.g. generated YARPC server types in `tangopb/tangopbmock/`).

To regenerate or add a mock:

```bash
mockgen -destination=<destdir>/<file>.go <package path> <Interface>,<Interface>
# or in the current package:
mockgen -destination=<destdir>/<file>.go . <Interface>
```

After regenerating, run `make gazelle` so the new file is picked up by Bazel.

### Naming Conventions

- **Directories**: singular (`mock/`, `controller/`, not `mocks/`, `controllers/`).
- **Files**: `{method}.go` for RPC handlers (e.g. `getchangedtargets.go`, `gettargetgraph.go`), `{package}.go` for the package's main type, `{file}_test.go` for tests.
- **Proto files**: `{service}.proto`.
- **Mock packages**: `{pkg}mock` or `mock` subdirectory — match the surrounding package's existing convention rather than introducing a new one.
- **README files**: Do not duplicate interface or type definitions as code blocks in READMEs. Describe behavior in prose and let readers navigate to the source. Only include code samples when explicitly instructed.
- **Markdown prose width**: Do not hard-wrap prose in Markdown docs (READMEs, design notes). Write one line per paragraph and one line per list item, and let the editor soft-wrap — hard wrapping at a fixed column renders as a narrow fixed-width column regardless of window size. Code blocks, tables, and ASCII diagrams keep their own line breaks.

### Makefile

The Makefile has a hand-written `help` target that lists available targets with descriptions. New targets should include a comment explaining their purpose.

### Common Make Targets

```bash
make build                            # Build all targets
make test                             # Run all tests
make test-integration                 # Run integration tests (requires a local repo; slow)
make bench                            # Run GetChangedTargets benchmarks (measurement only, not in CI)
make lint                             # Run golangci-lint
make cover                            # Run tests with coverage, write cover.out
make gazelle                          # Update BUILD.bazel files
make proto                            # Regenerate protobuf files
make clean                            # Clean Bazel cache
make clean-proto                      # Remove generated proto files
make run-server                       # Run the Tango YARPC server (127.0.0.1:8081)
make run-client-get-graph             # Drive the client against the running server
make run-client-changed-targets       # Drive get-changed-targets against the running server
make version                          # Show Bazel version
make help                             # Show all targets with descriptions
```

### CI and Validation

CI builds and tests every PR via GitHub Actions. Before committing, validate locally:

1. `make build` — ensures Bazel and Go compile.
2. `make test` — runs the full unit test suite.
3. `make gazelle` — ensure `BUILD.bazel` files are up to date.

If you modified `.proto` files or interface signatures, also run `make proto` and regenerate the relevant mocks.

**Commit and PR titles must follow the [Conventional Commits](https://www.conventionalcommits.org/) specification.** Use a type prefix (`feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `build`, `ci`, `perf`, `style`) followed by a scope and a short imperative subject — e.g. `feat(orchestrator): support remote build execution`, `fix(controller): surface readTreehash errors`, `docs(architecture): clarify cache ownership`. Breaking changes use `!` after the type/scope (e.g. `feat(storage)!: ...`) and explain the break in the body. This keeps the commit history machine-parseable for changelogs and release automation.

### Code Style

1. **Structured logging** — use `zap.Logger` with explicit `zap.Field`s. Never use `zap.SugaredLogger`, `Printf`, or unstructured `fmt` logging.
2. **Interfaces for behavior, structs for data** — interfaces for behavioral contracts (`Storage`, `RepoManager`, `Workspace`, `GraphRunner`, `Orchestrator`). Structs for data containers, configs, and params (`Config`, `Params`, `WorkspaceParams`).
3. **Value-oriented identities and configuration** — prefer values for identity structs, configs, params, and ordinary results. Use `(T, bool)` for optional value results when practical; pointers remain appropriate for optional payloads, mutation, or shared ownership.
4. **`Params` structs** — every non-trivial constructor takes a `Params` value (e.g. `controller.Params`, `orchestrator.Params`, `repomanager.Params`). New optional fields go on `Params` with a documented default, not as constructor overloads.
5. **Errors for failures, not control flow** — reserve `error` for unexpected or infrastructure failures. For expected outcomes use result types or `(T, bool)`. Avoid sentinel errors that represent non-failure states; `storage.ErrNotFound` exists specifically because "not found" is a legitimate cache-miss signal that callers must distinguish from real failures via `storage.IsNotFound(err)`.

### Error Classification (`core/errors`)

Errors are classified by **origin** (user vs infra) for metrics. The contract lives in `core/errors/errors.go`:

- `TangoError` — the internal error type, carrying the underlying error and an `ErrorCode`.
- `ErrorCode` values: `ErrorUser` (caused by client input), `ErrorInfra` (caused by infrastructure), `ErrorInfraRetryable` (transient infra failure the client may retry), `ErrorCancelled` (context cancellation, detected automatically by `GetErrorCode`).
- Constructors: `NewUser(err)`, `NewInfra(err)`, `NewInfraRetryable(err)`.

**Key rules:**

1. **Classify at the deepest boundary with semantic context.** Request validation and mapping classify bad client input as user errors. Orchestrator classifiers map known lease, git, and Bazel causes to user, infra, or retryable infra outcomes. Lower capability packages return plain wrapped errors and sentinels.
2. **Unclassified errors default to infrastructure failures.** Retryability must be supported by a known transient cause; do not mark an error retryable merely because repeating the request is convenient.
3. **Preserve the chain with `%w`.** `TangoError` implements `Unwrap`, so `errors.Is` and `errors.As` continue to find lower-level sentinels through classification wrappers.
4. **Complete the standard metrics lifecycle and convert errors at the wire boundary.** `metrics.Op.Complete` derives the finish histogram's `result` tag from `tangoerrors.GetErrorCode(err).String()`. The controller maps the final classified error to the RPC boundary, while `errors.Fields` provides structured log fields.

### Caching and Treehashes

The cache is the single most performance-sensitive piece of Tango. Cache identity and ownership are artifact-specific:

| Artifact | Current key dimensions | Read/write ownership |
|---|---|---|
| Treehash mapping | Short remote, base SHA, ordered change-request URL list | Controller reads for fast paths; orchestrator writes synchronously when storing a newly computed graph. |
| Target graph | Short remote, treehash, computation strategy, sorted extra exclusion regexes | Controller performs the initial read; orchestrator reads again after deriving the treehash and synchronously writes a computed miss. |
| Compared targets | Short remote, ordered treehash pair, sorted extra exclusion regexes | Controller reads with miss/corruption fallback and writes asynchronously, best-effort. |

Key rules:

1. **Use `core/cachekey` helpers exclusively.** Never construct cache paths inline. When a new input changes computed graph or comparison output, update or version the relevant key and add tests. Current keys do not encode every repository configuration or algorithm/schema version, so such changes require deliberate compatibility or invalidation decisions.
2. **Keep output-only options outside cache identities.** Store full graph and comparison payloads, then apply distance and field filtering while sending so presentation choices cannot poison shared entries.
3. **Distinguish misses from failures.** A not-found or corrupt entry is an expected recomputation path. Prefer to fail fast when either target-graph or compared-target cache access fails with a genuine infrastructure error rather than silently degrading cache reliability.
4. **Expect duplicate work on concurrent misses.** Tango does not promise singleflight, compare-and-swap, or exactly-once computation. Deterministic computation plus complete content keys makes repeated overwrites converge on the same value.
5. **`bypass_cache` skips reads, not writes.** It forces recomputation and overwrites the existing entry under the deterministic key.

### Cancellation and Background Work

- **Pass `ctx` everywhere** and check `ctx.Err()` periodically inside any loop whose body is cheap but iteration count is large (graph walks, BFS, metadata merges). The shared constant is `cancelCheckInterval`.
- **Cancellation is cooperative.** When fanning out (e.g. parallel graph fetches in `GetChangedTargets`), derive each goroutine's context from the parent and cancel siblings when one fails so resources aren't wasted on a result that will be discarded.
- **Background work that must outlive the request** (cache writes) uses the controller's `appCtx` — the application-lifetime context passed to `NewController`, which is cancelled on process shutdown but not by client disconnect. This ensures a cache write is not aborted mid-flight when a client disconnects, but is still cleaned up on graceful shutdown. Per-operation deadlines are the storage backend's responsibility, not the controller's. Goroutines using `appCtx` must be self-contained: their inputs must not be mutated by the foreground after the goroutine starts.

### Testing

- **Table-driven tests** — prefer `t.Run` subtests over individual test functions for related cases.
- **Avoid asserting on error messages** — assert on error type or use `require.Error`. Do not `assert.Contains(t, err.Error(), message)`.
- **No change detector tests** — don't assert on internal structure, field-for-field equality of generated types, or defaults that can shift without behavior changing. Test what the code *does*.
- **No `time.Sleep` for synchronization** — use channels, callbacks, condition variables.
- **Use testify for value assertions** — use `assert` / `require` for values and errors; `t.Fatal` remains appropriate for test control flow such as failed setup or timeout/select guards.
- **Mocks via `go.uber.org/mock`** — generated mocks (`*mock` subpackages) for interface-driven dependencies. Inline test doubles only when the interface is small and used by exactly one test.
- **Goroutine leaks** — long-running tests should use `goleak.VerifyNone(t)` (or `goleak.VerifyTestMain`) to catch leaked goroutines from fan-out or background cache writes.

### Common Workflows

**Add a new RPC method:**

1. Edit `proto/tango.proto` → `make proto`.
2. Add the handler in `controller/{method}.go` and tests in `controller/{method}_test.go`.
3. Wire the handler into the YARPC dispatcher in `example/main.go` (the generated `BuildTangoYARPCProcedures` already covers new methods on the service interface).
4. Regenerate any affected mocks; run `make gazelle`.

**Add a new storage backend:**

1. Create `core/storage/{impl}/{impl}.go` implementing `storage.Storage` and a constructor that returns the concrete type.
2. Add a config branch under `config.StorageConfig` and a `case` in `newStorage` in `example/main.go`.
3. Add tests under `core/storage/{impl}/{impl}_test.go`; reuse the conformance tests if/when extracted. Run `make gazelle`.

**Add a new graph computation strategy:**

1. Implement `graphrunner.GraphRunner` in a new file under `graphrunner/`.
2. Add a `ComputationStrategy` enum value in `proto/tango.proto`, `make proto`.
3. Wire selection in the orchestrator's runner construction.

**Add a new entity field:**

1. Add a comment describing the field's data semantics, optionality, units, and invariants.
2. Update proto and mapper boundaries if the field crosses the RPC surface, then add behavior-focused tests for every affected path.

**Add a new config field:**

1. Add the field to the relevant struct in `config/` with a YAML tag, a Go comment, and validation in `config.Parse` or the relevant validator.
2. If the field has a default, document it on the field and set it during parsing so callers do not observe an ambiguous zero value.
