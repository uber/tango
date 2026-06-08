# Example

A demonstration server that shows how to run Tango end-to-end. It boots a
YARPC/gRPC server on `127.0.0.1:8081`, wiring together config parsing,
storage, the repo manager, the orchestrator, and the controller. A companion
CLI client calls the server's streaming RPCs, and a query-bench tool exercises
the underlying Bazel query and target-hashing path without bringing up the
server.

## Configuration

The server reads `tango-config.yaml`. Top-level sections:

- `storage` — `type: memory` (default) or `type: disk` with a `root_path`.
- `repository` — remotes Tango is allowed to operate on, with default branch,
  excluded files, external-target handling, bzlmod, and per-query timeout.
- `service` — worker pool size, origin clone path, and per-worker checkout
  path. Both directories are created on start and removed on clean shutdown.

## Running

Start the server:

```bash
make run-server
```

Query the target graph at HEAD:

```bash
make run-client-get-graph \
  REMOTE=https://github.com/uber/tango.git \
  BASE_SHA=HEAD
```

Diff targets between two revisions:

```bash
make run-client-changed-targets \
  REMOTE=https://github.com/uber/tango.git \
  BASE_SHA=872881fd~1 \
  NEW_BASE_SHA=872881fd
```

The client supports two methods (`get-target-graph`, `get-changed-targets`)
and flags for limiting changed-target distance (`-max-distance`), cache bypass
(`-bypass-cache`), output detail (`-include-hashes`, `-include-tags`,
`-include-attributes`), and request URLs (`-request-urls`,
`-new-request-urls`). Run with `-h` for the full list.

## Benchmarking

The query-bench tool times the standard Tango query against a real Bazel
workspace and reports per-stage timings (query, hashing, response conversion):

```bash
bazel run //example/cmd/query-bench -- --workspace /path/to/repo --runs 3
```
