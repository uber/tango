# Example

A demonstration server that shows how to run Tango end-to-end. It boots a YARPC/gRPC server on `127.0.0.1:8081`, wiring together config parsing, storage, the repo manager, the orchestrator, and the controller. A companion CLI client calls the server's streaming RPCs, and a query-bench tool exercises the underlying Bazel query and target-hashing path without bringing up the server.

## Configuration

The server reads [`tango-config.yaml`](tango-config.yaml). Unknown fields are rejected, so configuration typos fail at startup.

### Storage

| Field | Required/default | Description |
|---|---|---|
| `storage.type` | Optional; defaults to `memory` | Storage backend. Accepted values are `memory` and `disk`. |
| `storage.disk.root_path` | Required when `storage.type` is `disk` | Directory used by the disk storage backend. |

### Repositories

`repository` is a list of per-repository settings. Each `remote` must be unique and must exactly match the remote sent by clients.

| Field | Required/default | Description |
|---|---|---|
| `repository[].remote` | Required | URL Tango clones and uses to look up this entry. |
| `repository[].full_hash_repos` | Optional; defaults to `[]` | External repositories whose individual files should be hashed instead of sharing the repository-rule hash. The main repository is always fully hashed. |
| `repository[].excluded_files` | Optional; defaults to `[]` | Regular expressions for target labels to exclude from the hashed graph. |
| `repository[].bzlmod_enabled` | Optional; defaults to `true` | Whether the repository uses Bzlmod. Set to `false` for legacy WORKSPACE dependency resolution. |
| `repository[].bazel_command_path` | Optional; defaults to empty | Bazel executable path. When empty, Tango downloads and caches Bazelisk automatically. |
| `repository[].bazel_extra_args` | Optional; defaults to `[]` | Additional arguments passed to `bazel query` after the subcommand. |
| `repository[].bazel_startup_options` | Optional; defaults to `[]` | Bazel startup options placed before the `query` subcommand. |
| `repository[].stream_bazel_logs` | Optional; defaults to `false` | Whether Bazel stderr is streamed to the server process while a query runs. |
| `repository[].query_timeout_seconds` | Optional; defaults to `600` for omitted or non-positive values | Bazel query timeout in seconds. |
| `repository[].seed_attributes` | Optional; defaults to `[]` | Attribute names that can make a target directly changed. When empty, all attributes are considered. |
| `repository[].all_targets_files` | Optional; defaults to `[]` | Exact repo-relative paths whose content changes make every target in the newer graph changed. |

### Service

| Field | Required/default | Description |
|---|---|---|
| `service.max_worker_pool_size` | Required; must be greater than `0` | Maximum concurrent requests per repository. |
| `service.workspaces_root_path` | Required | Root directory for origin clones and worker checkouts. The example server creates it at startup and removes it on clean shutdown. |
| `service.max_message_bytes` | Optional; defaults to `4250000` for omitted or non-positive values | Maximum serialized bytes per streamed gRPC message. |
| `service.graph_format` | Optional; defaults to `gob` | Cached target-graph format. Accepted values are `gob` and `tgb`. |
| `service.shadow_compare` | Optional; defaults to `false` | With `graph_format: tgb`, also runs the incumbent comparison in the background and reports mismatches without changing the served result. |

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

The client supports two methods (`get-target-graph`, `get-changed-targets`) and flags for limiting changed-target distance (`-max-distance`), cache bypass (`-bypass-cache`), output detail (`-include-hashes`, `-include-tags`, `-include-attributes`), and request URLs (`-request-urls`, `-new-request-urls`). Run with `-h` for the full list.

Change requests are identified by canonical change URIs of the form `github://{host[:port]}/{org}/{repo}/pull/{pr}/{head_sha}`, per the [change-URI RFC](https://github.com/uber/submitqueue/blob/main/doc/rfc/change-uri.md) — for example `github://github.com/uber/tango/pull/123/c3a4b5d6e7f80912a3b4c5d6e7f80912a3b4c5d6`. The head SHA is the PR's head commit at submission time; it pins the exact code state applied on top of the base revision and forms the cache identity. The bundled native orchestrator rejects non-canonical spellings (uppercase host, abbreviated SHA, missing host); custom Orchestrator implementations may accept formats of their own.

## Benchmarking

The query-bench tool times the standard Tango query against a real Bazel workspace and reports per-stage timings (query, hashing, response conversion):

```bash
bazel run //example/cmd/query-bench -- --workspace /path/to/repo --runs 3
```
