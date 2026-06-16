# Tango (Target Analyzer) Repository Guide for Claude

## Key Concepts

Tango (**Ta**rget A**n**alyzer in **Go**) is a standalone library and service that fetches and compares Bazel target graphs across revisions of a repository. It answers two related questions:

1. **What does the target graph look like at a given revision?**
2. **Which targets changed between two revisions, and what is each changed target's BFS distance from the nearest direct cause?**

It is designed to run independently of the monorepo it analyzes — the only inputs are a remote URL, a base SHA, and optionally a set of change requests (PR/diff URLs + commit SHAs) to layer on top.

### Core design properties

1. **Content-addressable caching** — graphs and change results are keyed by the git **treehash** of the materialized workspace, not by the SHA. Two requests that resolve to the same tree share the same cache entry, even if they came from different branches or commit chains. The treehash itself is also cached by `BuildDescription` so cache lookups don't require re-materializing a workspace.
3. **Streaming, chunked responses** — target graphs and change results are split into chunks to stay within gRPC per-message limits. Metadata mappings (target IDs → names, rule types, tags, attributes) may also span multiple chunks; consumers must merge them before use.
4. **ID-mapped payloads** — over the wire, targets reference each other (and their rule types, tags, attributes) by `int32` IDs into per-stream metadata maps. Comparison code re-maps both inputs into a canonical per-call namespace and prunes unreferenced metadata entries before sending. IDs are not guaranteed to be consistent across multiple target graphs.
5. **Always-on cancellation** — Both request-bound and application-bound cancellation signals are honored. Every long-running loop (graph walk, BFS, metadata merge) checks `ctx.Err()` on a fixed cadence. A client disconnect cancels the stream's context and unwinds the work.

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
├── graphrunner/                    # Strategy-pluggable target-graph computation (native / shell)
│   └── mock/
├── config/                         # YAML config parsing and validation (storage, service, repository)
├── core/                           # Reusable infrastructure with no domain dependencies
│   ├── git/                        # Git CLI wrapper (clone, fetch, checkout, rev-parse, ...)
│   ├── repomanager/                # Per-repo worker-pool / clone manager
│   ├── storage/                    # Blob storage interface and impls (in-memory, disk)
│   ├── workspace/                  # Workspace abstraction over a git checkout, request application
│   └── ...                         # bazel, common, execcmd, itg, targethasher, ...
├── example/                        # Runnable server + client and benchmark CLI
│   ├── client/
│   └── cmd/query-bench/
└── tools/                          # Bazelisk wrapper and tooling
```

The top-level split is by **responsibility**, not by domain: `controller/` handles the RPC surface, `orchestrator/` drives the end-to-end pipeline, `graphrunner/` owns the strategy for computing a graph, and `core/` holds reusable building blocks that no other layer should bypass. Every concrete dependency that crosses a layer boundary (storage, git, bazel, graph runner) is an interface so it can be mocked or swapped per repository / per deployment.

### Controllers

The controller is the YARPC service implementation. It owns the transport-adjacent concerns: request validation, response chunking, cancellation handling, fan-out across revisions, and metrics emission. It does **not** own workspace creation, git operations, or graph computation — those belong to the orchestrator and below.

Each RPC method follows the same shape:
1. Increment a `calls` counter; defer `success` / `failure` counters and a `failure_type` tag based on `classifyError`.
2. Validate the request; reject with a `ClassifiedError` of type `ErrorTypeUser` on bad input.
3. Drive the orchestrator to obtain the given number of target graphs.
5. Stream the result to the client.

### Orchestrator

The orchestrator's sole purpose is to **produce a target graph for a given revision** — given a `BuildDescription` (remote + base SHA + optional change requests), return either a cached graph or one freshly computed against a materialized workspace. Everything above it (controller, RPC fan-out, comparison) treats the orchestrator as an opaque "give me the graph for this revision" call.

The bundled `nativeOrchestrator` (under `orchestrator/native_orchestrator.go`) is an **example implementation** intended for standalone / OSS use. It leases a workspace from the local `RepoManager`, checks out the base SHA, applies each change request via the `workspace.Request` abstraction, computes the treehash, consults the cache, and falls through to `graphrunner` on a miss. It exists primarily to make the OSS build runnable end-to-end and to anchor tests.

**Most production monorepo setups will need to provide their own `Orchestrator` implementation** that integrates with the host CI system (e.g. Buildkite, internal build infrastructure) instead of managing local clones. CI-driven environments already own:

- the materialized source tree at the target revision (no need to clone or apply patches locally),
- distributed caches keyed by content hashes that should be consulted before recomputing,
- worker pools, retry policies, and timeouts that differ from the in-process model used by `nativeOrchestrator`.

A custom orchestrator satisfies the same `orchestrator.Orchestrator` interface and is wired into the controller in place of the native one. It is the right seam to plug in remote build execution, CI-managed checkouts, or organization-specific caching — the controller and `graphrunner` stay unchanged.

Whichever implementation is used, the orchestrator is responsible for **classifying** errors via `common.WithReason(reason, errorType, err)` so the metrics pipeline can tag failures with a stable `failure_reason` and `failure_type`.

### Graphrunner

`graphrunner.GraphRunner` is the **interface a CI-integrated `Orchestrator` implementation calls to compute a target graph** from an already-materialized workspace. The orchestrator's job is to obtain the source tree (via local clones, CI-managed checkouts, or remote execution); the graphrunner's job is to turn that tree into a per-target hashed `Result`. The split keeps tree provisioning and graph computation independently swappable.

The contract is intentionally narrow: given a `workspace.Workspace`, return `(targethasher.Result, error)`. A graphrunner does not know about cache keys, storage, transport, or the request that triggered it — those concerns live in the orchestrator. This is what lets a CI-driven orchestrator reuse the same `GraphRunner` implementations the OSS path uses, while replacing every other moving part around it.

New strategies plug in by satisfying the same interface. Keep them strategy-agnostic about how the workspace was assembled and what will be done with the result.

### Extensions and interfaces

Pluggable interfaces live in their own package with a single responsibility:

- `core/storage.Storage` — blob get/put/exists/list keyed by string
- `core/git.Interface` — git CLI operations
- `core/repomanager.RepoManager` — workspace lease/release
- `core/workspace.Workspace` + `core/workspace.Request` — checkout, apply
- `graphrunner.GraphRunner` — compute a graph from a workspace
- `orchestrator.Orchestrator` — top-level entry point

**Design interfaces for the technology *space*, not the implementation in front of you.** The contract must be cheaply satisfiable by every plausible backend, not just the one being built today. For example, the `Storage` interface offers `Get`/`Put`/`Exists`/`List` keyed by a string — primitives that a disk, an in-memory map, S3, GCS, or a CDN can all satisfy without contortion.

Common over-constraints to avoid:
- **Server-side filters / queries** — push filtering and aggregation to the caller; keep storage responsible only for "get/put by key" semantics.
- **Batch atomicity** (multi-blob writes as one transaction) — many backends can't do this. Prefer single-blob primitives + caller loops + content-addressable keys for idempotency.
- **Strict ordering / exactly-once** for any background work — make consumers (and cache writes) idempotent by deriving keys from content (treehashes), not request order.
- **Synchronous, low-latency calls** for things that may run remotely — design for retry/backoff and timeouts. **Per-operation deadlines are the backend's responsibility, not the controller's** — the controller is backend-agnostic and must not encode any one implementation's I/O budget.

When in doubt, ask: *"If the next implementation were S3 / GCS / a remote RPC service / an in-memory map, could it satisfy this signature without contortion?"* If the answer is no, simplify the contract.

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

Targets stay **alphabetically sorted** and each has a `## Description` for the auto-generated `make help`:

```makefile
build: ## Build all targets
	@$(BAZEL) build //...
```

### Common Make Targets

```bash
make build                            # Build all targets
make test                             # Run all tests
make gazelle                          # Update BUILD.bazel files
make proto                            # Regenerate protobuf files
make clean                            # Clean Bazel cache
make clean-proto                      # Remove generated proto files
make run-server                       # Run the Tango YARPC server (127.0.0.1:8081)
make run-client-get-graph             # Drive the client against the running server
make run-client-changed-targets       # Drive get-changed-targets against the running server
make help                             # Show all targets
```

### CI and Validation

CI builds and tests every PR via GitHub Actions. Before committing, validate locally:

1. `make build` — ensures Bazel and Go compile.
2. `make test` — runs the full unit test suite.
3. `make gazelle` — ensure `BUILD.bazel` files are up to date.

If you modified `.proto` files or interface signatures, also run `make proto` and regenerate the relevant mocks.

**Commit and PR titles must follow the [Conventional Commits](https://www.conventionalcommits.org/) specification.** Use a type prefix (`feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `build`, `ci`, `perf`, `style`) followed by an optional scope and a short imperative subject — e.g. `feat(orchestrator): support remote build execution`, `fix(controller): surface readTreehash errors`, `docs: add CLAUDE.md`. Breaking changes use `!` after the type/scope (e.g. `feat(storage)!: ...`) and explain the break in the body. This keeps the commit history machine-parseable for changelogs and release automation.

### Code Style

1. **Structured logging** — `zap.SugaredLogger` with `Debugw`/`Infow`/`Warnw`/`Errorw(msg, key, val, ...)` or `zap.Logger` with explicit `zap.Field`s. Never log via `Printf` or unstructured `fmt`.
2. **Interfaces for behavior, structs for data** — interfaces for behavioral contracts (`Storage`, `RepoManager`, `Workspace`, `GraphRunner`, `Orchestrator`). Structs for data containers, configs, and params (`Config`, `Params`, `WorkspaceParams`).
3. **Value types over pointers** — prefer value types for structs, configs, and return values. Use `(T, bool)` to signal absence instead of `*T`. Pointers only when mutation or shared ownership is needed.
4. **`Params` structs** — every non-trivial constructor takes a `Params` value (e.g. `controller.Params`, `orchestrator.Params`, `repomanager.Params`). New optional fields go on `Params` with a documented default, not as constructor overloads.
5. **Errors for failures, not control flow** — reserve `error` for unexpected or infrastructure failures. For expected outcomes use result types or `(T, bool)`. Avoid sentinel errors that represent non-failure states; `storage.NotFoundError` exists specifically because "not found" is a legitimate cache-miss signal that callers must distinguish from real failures.

### Error Classification (`core/common`)

Errors are classified by **origin** (user vs infra) for metrics. The contract lives in `core/common/errors.go`:

- `ErrorTypeUser` — caused by client input (validation failure, unknown repo, malformed request, client disconnect).
- `ErrorTypeInfra` — caused by infrastructure (storage failure, git failure, bazel failure, timeout, panic).

**Key rules:**

1. **Wrap at the failure site** with `common.WithReason(reason, errorType, err)` so the metrics tag carries a stable `failure_reason` and `failure_type`.
2. **The deepest layer that knows the classification wraps the error.** Lower layers (storage, git, bazel) return plain errors with their own sentinels (`storage.NotFoundError`). The orchestrator and controller decide whether a given failure is user-caused or infra-caused — the same `storage.NotFoundError` may be a cache miss (silent, not an error) in one path and a `failureReasonStorage` infra error in another.
3. **`failureReason*` constants live next to the package that emits them** (e.g. `orchestrator/errors.go`, `controller/errors.go`). Add a new constant rather than reusing a vague existing one; the metric is only useful if the reason is specific enough to act on.
4. **Errors flow through `errors.Is` / `errors.As`** — `ClassifiedError` implements `Unwrap`, so wrapping with `WithReason` preserves the underlying sentinels (e.g. callers can still `errors.As(..., &storage.NotFoundError{})`).

### Caching and Treehashes

The cache is the single most performance-sensitive piece of Tango. Two rules:

1. **Cache keys are content-addressable, derived from the git treehash** of the materialized workspace (base + applied requests). This is what makes the cache safe across branches and PR retargets: identical trees share entries regardless of how they were assembled. Helpers in `core/common` (`GetGraphByTreeHash`, `GetComparedTargetsCachePath`, `GetTreehashCachePath`) own the key shape — never construct cache paths inline.
2. **Treat the cache as best-effort.** Reads tolerate `NotFoundError` silently; other storage errors are logged but don't fail the request — fall through to recompute.

### Cancellation and Background Work

- **Pass `ctx` everywhere** and check `ctx.Err()` periodically inside any loop whose body is cheap but iteration count is large (graph walks, BFS, metadata merges). The shared constant is `cancelCheckInterval`.
- **Cancellation is cooperative.** When fanning out (e.g. parallel graph fetches in `GetChangedTargets`), derive each goroutine's context from the parent and cancel siblings when one fails so resources aren't wasted on a result that will be discarded.
- **Background work that must outlive the request** (cache writes, telemetry flushes) uses `context.WithoutCancel(ctx)` — this preserves request-scoped values (tracing, identity) without inheriting the deadline or cancellation signal. Such goroutines must be self-contained: their inputs must not be mutated by the foreground after the goroutine starts.

### Testing

- **Table-driven tests** — prefer `t.Run` subtests over individual test functions for related cases.
- **Avoid asserting on error messages** — assert on error type or use `require.Error`. Do not `assert.Contains(t, err.Error(), message)`.
- **No change detector tests** — don't assert on internal structure, field-for-field equality of generated types, or defaults that can shift without behavior changing. Test what the code *does*.
- **No `time.Sleep` for synchronization** — use channels, callbacks, condition variables.
- **Use testify** — `assert` / `require` instead of `t.Fatal()`.
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

**Add a new entity / config field:**

1. Add the field to the relevant struct in `config/` with a YAML tag, a Go comment, and validation in `config.Parse` (or the relevant validator).
2. If the field has a default, document it on the field and set it in `Parse` — do not let callers see an unset zero value.
