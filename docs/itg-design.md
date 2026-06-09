# Incremental Target Graph (ITG) — Design Document

## Overview

The Incremental Target Graph (ITG) system computes a full Bazel target graph for a given
revision without running a full `bazel query`. Instead of re-querying the entire workspace, it
finds the nearest cached graph and applies incremental updates only to the packages that
changed. This can be an order of magnitude faster than a full Bazel query for small-to-medium
changesets.

## Problem Statement

Tango's core operation is computing a target graph at a given revision (BaseSha + optional user
diffs). A full `bazel query //...` is expensive — it scans the entire workspace and can take
minutes for large monorepos. Most changes touch only a handful of packages, so the vast
majority of that work is redundant. ITG exploits this observation by:

1. Keeping a cache of graphs at historical main-branch commits.
2. Replaying only the changes between the cached commit and the requested revision.

## High-Level Architecture

```
GetTargetGraph (orchestrator)
       │
       ├─ cache hit (treehash)? ──► return cached graph
       │
       ├─ ITG available?
       │    └─ IncrementalGraphProvider.GetGraph()
       │         ├─ Step 0: AnalyzeChange (fail fast on critical files)
       │         ├─ Step 1: zero_revision → BaseSha  (origin clone)
       │         └─ Step 2: BaseSha → TargetRef      (workspace clone)
       │              └─ success ──► write to storage, return
       │
       └─ fallback: full bazel query
            └─ no diffs? SeedCache(result) ──► populate ITG cache
```

## Key Components

### Provider (`core/itg/itg.go`)

The central orchestrator of the ITG algorithm. It holds:

- **`p.git` / `p.bazel`**: clients pointing at the *origin clone* — a stable checkout used only
  for Step 1 historical computation. The origin clone is checked out to different commits during
  Step 1; no user diffs are ever applied there.
- **`p.cache`**: storage-backed cache of `OptimizedGraph` blobs, keyed by remote + commit
  timestamp + SHA.
- **`p.analyzer`**: pattern-matching rules for classifying files (build files, critical files,
  ignored files).
- **`p.bazelFactory` / `p.hasherFactory`**: per-request factories so each workspace gets its
  own Bazel client and source hasher.

### OptimizedGraph (`core/itg/graph/graph.go`)

An in-memory, compact representation of the target graph. All strings (target names, rule
types, tag names, attribute names/values) are interned to integer IDs to reduce memory
footprint and make serialization fast.

Key fields:
- `OptimizedTargets map[int]*OptimizedTarget` — the full set of build targets.
- `ExternalRuleTargets map[int]*OptimizedTarget` — subset of external-repo rules.
- Bidirectional string↔int mappings for targets, rule types, tags, and attributes.
- `NextTargetID int` — monotonically increasing counter for new targets.

### OptimizedTarget

Represents a single build target:
- `ID int` — stable numeric identifier within this graph.
- `Hash []byte` — SHA-1 of the target's content plus all transitive dependencies.
- `HashWithoutDeps []byte` — SHA-1 of only the rule's own attributes (not deps).
- `Deps IntSet` — direct dependency IDs.
- `ReverseDeps IntSet` — targets that directly depend on this target.
- `RuleType int` — interned rule type ID (e.g. `py_library`).
- `Root bool` — true if no other target depends on this one.
- `External bool` — true if this target lives in an external repository.

### Cache (`core/itg/cache/cache.go`)

Wraps the underlying `storage.Storage` interface. Cache entries are stored at:

```
itg/{remote}/{YYYY-MM-DD}/{unix_timestamp}_{commit_sha}
```

The date+timestamp structure enables efficient *floor key* lookups: given a target commit
time T, find the most recent cached graph at or before T via a binary search over entry
names. Graphs are serialized with `encoding/gob`.

### ChangeAnalyzer (`core/itg/changeanalyzer/analyzer.go`)

Classifies file changes using configurable regex patterns:

| Category | Examples | Effect |
|---|---|---|
| `BuildFile` | `BUILD`, `BUILD.bazel` | Package must be re-queried |
| `CriticalFile` | `WORKSPACE`, `MODULE.bazel` | ITG unsupported — full query required |
| `ExtensionFile` | `*.bzl` | Package must be re-queried |
| `RegularFile` | `*.go`, `*.py` | Hash recomputation only |
| `IgnoredFile` | `METADATA` | No-op |

`AnalyzeChange` runs an upfront `git diff` and returns a `ChangeComplexity` that determines
whether ITG can proceed.

## Algorithm: GetGraph

### Step 0 — Fail-fast Change Analysis

Before touching the origin clone or running any Bazel command, `GetGraph` validates the full
range `cacheKey.BaseSha → TargetRef` using the *workspace* git (which already has user diffs
applied). If any critical file was changed, `isSupportedChangeComplexity` returns false and ITG
aborts immediately, letting the orchestrator fall back to a full Bazel query.

```
wsAnalyzer.AnalyzeChange(cacheKey.BaseSha, "HEAD")
  → NoChange | RegularFilesModificationOnly | ReparsePackagesNeeded → proceed
  → FullCalculationRequired                                          → abort
```

### Step 1 — Historical Graph: `zero_revision → BaseSha`

Using the **origin clone** (never touched by user diffs):

1. **Cache lookup**: `cache.FloorKey(remote, baseShaCommitSecond)` finds the nearest cached
   graph at or before BaseSha's commit timestamp via binary search. Returns `EmptyKey` if the
   cache is cold.
2. **Load cached graph**: `cache.Get(cacheKey)` deserializes the `OptimizedGraph` from
   storage. An empty graph is used on a cold cache.
3. **Incremental update**: `calculateGraphIncrementally(cacheGraph, cacheKey.BaseSha, BaseSha,
   originGit, originBazel)` advances the graph from the cached commit to BaseSha:
   - `git checkout BaseSha` on the origin clone.
   - `git diff --name-status cacheKey.BaseSha BaseSha` to enumerate changed files.
   - `parseChanges` maps each file to its package, producing `ChangedPkgs`, `DeletedPkgs`,
     `DeletedSrcFiles`.
   - One Bazel query is issued per unique changed package (combined into a single query with
     `+` unions).
   - `OptimizedGraph.UpdateGraph` integrates the query result and recomputes hashes for
     invalidated targets.

### Step 2 — User Diffs: `BaseSha → TargetRef`

If user diffs exist (BaseSha ≠ TargetRef):

1. **Concurrent cache write**: the BaseSha graph is copied and written to the ITG cache in a
   goroutine so Step 2 can begin immediately.
2. **Incremental update**: `calculateGraphIncrementally(baseShaGraph, BaseSha, "HEAD",
   wsGit, wsBazel)` runs against the **workspace** clone (already at TargetRef). This avoids
   checking out anything in the origin clone, keeping the two concerns isolated.

If no user diffs exist (BaseSha == TargetRef), the result is returned directly after the async
cache write — Step 2 is skipped.

### calculateGraphIncrementally (detail)

```
1. git checkout targetRef           (origin clone for Step 1; workspace is already there for Step 2)
2. git diff --name-status baseRef targetRef
3. If no changes → return baseGraph unchanged
4. If |changes| ≥ MinChangedFilesForGitHash → git ls-tree (batch file hashing)
5. parseChanges → UpdateGraphInput{ChangedPkgs, DeletedPkgs, DeletedSrcFiles}
6. Combine ChangedPkgs into a single bazel query: "//pkg1/... + //pkg2/... + ..."
7. bazel.ExecuteQuery(query)
8. hasher := hasherFactory(workspaceRoot, knownHashes)
9. baseGraph.UpdateGraph(ctx, hasher, updateInput)   ← mutates in place
10. return baseGraph
```

## Graph Update and Hash Computation

`UpdateGraph` (`core/itg/graph/update.go`) integrates a Bazel query result into the existing
`OptimizedGraph`. It operates in several passes:

### Pass 1 — Process external rule targets

External rules (targets in `@repo//...`) are upserted first. Any reverse dependency that
references an updated external rule is invalidated.

### Pass 2 — Compute available hashes

For each target returned by Bazel:
- **Source files**: hashed from disk (or from the `knownHashes` map if git ls-tree was used).
- **Package groups**: hashed by target name.
- **Generated files**: deferred (hash inherited from their single dependency).
- **Rules**: `HashWithoutDeps = SHA-1(sorted rule attributes)`.

### Pass 3 — Upsert regular targets

Targets are topologically sorted (dependencies before dependents) and processed in order:
- **New target**: inserted into the graph; marked as needing hash computation.
- **Existing target with changed deps**: reverse-dep pointers are updated; upstream hashes
  invalidated.
- **Existing target with same deps**: only hash recomputed if explicitly invalidated.

### Pass 4 — Remove deleted targets

Targets belonging to `DeletedPkgs` or referencing files in `DeletedSrcFiles` are removed.
Their reverse-dep pointers are cleaned up and propagated hashes are invalidated upward.

### Hash computation (recursive)

```
computeHash(target):
  if already computed → return
  mark as "visiting" (detect cycles)
  if source file / package group → use precomputed hash
  if generated file → copy dep's hash
  if rule:
    h = HashWithoutDeps
    for dep in sorted(Deps):
      h = SHA-1(h || computeHash(dep))
    Hash = h
```

Invalidation (`invalidateHashRecursively`) marks a target and all its transitive reverse
dependents as needing recomputation.

## Integration with the Orchestrator

`nativeOrchestrator.GetTargetGraph` (`orchestrator/native_orchestrator.go`) drives ITG:

```
1. Lease workspace, checkout BaseSha, apply user requests.
2. Compute treehash of HEAD^{tree}.
3. Cache lookup: storage.NewGraphReader(treehashPath) → hit? return immediately.
4. ITG fast path (if incrementalProvider != nil):
     itgResult, err := incrementalProvider.GetGraph(ctx, GetGraphRequest{...})
     if err == nil:
       storage.WriteGraphStream(treehashPath, itgResult.TargetRefGraph)
       mark computed = true
5. If !computed: full bazel query via graphRunner.Compute()
     - storage.WriteGraphStream() and incrementalProvider.SeedCache() run in parallel.
     - SeedCache is only called when no diffs were applied (Requests is empty).
6. Map BaseSha build description → treehash in storage for future fast lookup.
```

### SeedCache

When the orchestrator runs a full Bazel query on a clean main-branch commit (no user diffs),
`SeedCache` is called to populate the ITG cache with the result. This ensures the cache is
populated even when no prior ITG computation has occurred, bootstrapping future incremental
requests.

## Proto Serialization

The `OptimizedGraph` is serialized to the gRPC streaming format in 1000-target chunks
(`pb.GetTargetGraphResponse`) with a final metadata message carrying all string↔int dictionaries.
This allows clients to stream large graphs without buffering the entire response.

## Configuration

```go
type Config struct {
    WorkspaceRoot             string   // absolute path to origin clone workspace root
    BuildFilePatterns         []string // regexps matching BUILD files
    CriticalFilePatterns      []string // regexps matching files that require full query
    IgnoredFilePatterns       []string // regexps matching files whose changes are ignored
    MinChangedFilesForGitHash int      // threshold for switching to git ls-tree hashing
}
```

`MinChangedFilesForGitHash` controls a performance trade-off: below the threshold, files are
hashed individually from disk; above it, `git ls-tree` provides all hashes in one call, which
is faster when many files are touched.

## Failure Modes and Fallbacks

| Condition | Behavior |
|---|---|
| Critical file changed (WORKSPACE, MODULE.bazel) | Abort ITG; orchestrator falls back to full query |
| Cache is empty / floor key not found | Abort ITG; orchestrator falls back to full query |
| Bazel query fails | Error propagated; orchestrator falls back to full query |
| Cache write fails | Logged, non-fatal; ITG result still returned |
| SeedCache fails | Logged, non-fatal; full query result still stored |
| Git fetch fails | Error propagated; entire request fails |

ITG never silently returns a wrong graph — all failures are explicit errors that allow the
orchestrator to retry with the authoritative full-query path.

## Concurrency Design

- The **origin clone** is checked out during Step 1. A `defer git.Checkout(ctx, curRef,
  "--force")` restores the original HEAD after `GetGraph` returns, ensuring re-entrancy is
  safe if the provider is called serially.
- The **workspace clone** is never checked out by ITG — it is always assumed to be already at
  TargetRef with diffs applied. This isolation means Step 1 and Step 2 can use different git
  instances without interference.
- The BaseSha graph **cache write** is launched in a goroutine and runs concurrently with
  Step 2. A `Copy()` is made first to avoid a data race between the gob encoder and the Step 2
  mutator.
- The orchestrator uses a `sync.WaitGroup` to run `WriteGraphStream` and `SeedCache` in
  parallel after a full Bazel query.

## Performance Characteristics

| Operation | Full Query | ITG (small change) |
|---|---|---|
| Bazel query scope | Entire workspace | Changed packages only |
| Hash computation | All targets | Invalidated targets only |
| Cache lookup | N/A | O(log n) binary search |
| Graph serialization | Full gob write | Delta write (only BaseSha) |

For a change touching 5 packages in a 50,000-target monorepo, ITG may issue 5 Bazel queries
covering a few hundred targets instead of one query covering all 50,000.

## Sequence Diagram

```
Client          Orchestrator        ITG Provider        Origin Clone    Workspace
  │                  │                    │                   │              │
  │ GetTargetGraph   │                    │                   │              │
  │─────────────────►│                    │                   │              │
  │                  │ Lease + Checkout   │                   │              │
  │                  │────────────────────────────────────────────────────► │
  │                  │ ApplyRequests      │                   │              │
  │                  │────────────────────────────────────────────────────► │
  │                  │ treehash cache hit?│                   │              │
  │                  │ (miss)             │                   │              │
  │                  │ GetGraph()         │                   │              │
  │                  │───────────────────►│                   │              │
  │                  │                    │ FloorKey          │              │
  │                  │                    │──────────────────►│              │
  │                  │                    │ AnalyzeChange(ws) │              │
  │                  │                    │─────────────────────────────────►│
  │                  │                    │ Step 1: checkout BaseSha          │
  │                  │                    │──────────────────►│              │
  │                  │                    │ git diff + bazel query            │
  │                  │                    │──────────────────►│              │
  │                  │                    │ UpdateGraph       │              │
  │                  │                    │ [async] Put(cache)│              │
  │                  │                    │ Step 2: git diff  │              │
  │                  │                    │─────────────────────────────────►│
  │                  │                    │ bazel query (ws)  │              │
  │                  │                    │─────────────────────────────────►│
  │                  │                    │ UpdateGraph       │              │
  │                  │ TargetRefGraph     │                   │              │
  │                  │◄───────────────────│                   │              │
  │                  │ WriteGraphStream   │                   │              │
  │ GraphReader      │                    │                   │              │
  │◄─────────────────│                    │                   │              │
```

## Limitations

- **WORKSPACE / MODULE.bazel changes** are not supported incrementally. Any change to these
  files triggers a full query. External dependency graph changes (e.g., adding a new `go_repository`)
  are therefore not handled by ITG.
- **Cold cache** requires a seed from a full Bazel query before ITG can serve any requests.
  The bootstrapping path is the orchestrator's `SeedCache` call after a successful full query on
  a clean main-branch commit.
- **Origin clone serialization**: Step 1 checks out the origin clone, so concurrent `GetGraph`
  calls that share a single `Provider` instance must be serialized or use separate origin clones.
  The current design assumes serial access to the origin clone.
- **Cycle detection** in hash computation uses a sentinel (empty hash) rather than a full DFS
  cycle check. Bazel's own build graph should be acyclic, but malformed query results could
  cause undefined behavior.
