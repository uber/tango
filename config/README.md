# Tango Configuration

Tango is configured via a single YAML file. Pass the file path to `config.Parse` at startup.

---

## `service`

Controls how Tango operates at the service level — how many concurrent requests it can handle per repository, where it stores clones and worker checkouts on disk, and how it chunks responses over gRPC streams.

```yaml
service:
  max_worker_pool_size: 5
  workspaces_root: "/var/tango"
  # streaming:
  #   max_num_targets: 250
  #   max_num_changed_targets: 125
  #   max_num_metadata_entries: 50000
```

| Field | Required | Type | Default | Description |
|---|---|---|---|---|
| `max_worker_pool_size` | **Yes** | `int` | | Max number of concurrent requests per repository. Each worker is a lightweight local clone (hardlinked to the origin, not a full copy) that handles one request at a time — setting this to 5 means up to 5 concurrent graph computations per repo, and additional requests queue until a worker is free. A good starting point is the expected peak concurrent requests per repo. Must be greater than 0. |
| `workspaces_root` | **Yes** | `string` | | Root directory where Tango stores repository clones and worker checkouts. Required because Tango needs a known location to clone repositories and create worker checkouts — without it, there is no disk space allocated for graph computation. Choose a location with enough disk space to hold one origin clone plus N worker checkouts (where N is `max_worker_pool_size`) per configured repository. Use a persistent location in production — if this directory is lost, Tango re-clones from the remote on the next request, which can be slow for large repositories. Layout: `<workspaces_root>/<repo>/` for origin clones and `<workspaces_root>/.workers/<repo>/worker-{1..N}/` for worker checkouts. |
| `streaming.max_num_targets` | No | `int` | `250` | Max number of target entries per gRPC stream message (see `OptimizedTarget` in `proto/tango.proto`). Keep messages under gRPC's default 4MB receive limit — raise this only if the client is configured to accept larger messages and you want fewer round-trips. |
| `streaming.max_num_changed_targets` | No | `int` | `125` | Max number of changed target entries per stream message (see `ChangedTarget` in `proto/tango.proto`). Same tradeoff as `max_num_targets`. Default is lower because each entry carries both old and new targets (~2x the size). |
| `streaming.max_num_metadata_entries` | No | `int` | `50000` | Max number of ID-to-name mapping entries per stream message (see `Metadata` in `proto/tango.proto`). Large monorepos can have hundreds of thousands of metadata entries — lowering this value splits them across more messages, reducing peak memory per message. |

## `repository`

A list of repository entries. Each entry tells Tango how to clone, and query for a specific repository — including which Bazel settings to use.

```yaml
repository:
  - remote: "https://github.com/uber/tango.git"
    bzlmod_enabled: true
    # query_timeout_seconds: 600
```

| Field | Required | Type | Default | Description |
|---|---|---|---|---|
| `remote` | **Yes** | `string` | | The URL used to `git clone` the repository. Tango clones from this URL and uses it as the lookup key for per-repo settings. Must be unique across all entries and match exactly what clients send in `BuildDescription.remote`. |
| `bzlmod_enabled` | No | `bool` | `true` | Whether this repository uses Bzlmod for external dependency management. Bzlmod is enforced in Bazel 9+. Set to false only for repositories still using WORKSPACE — when disabled, `//external:all-targets` is added to Bazel queries. Setting this incorrectly causes query failures — it must match the repository's actual dependency management setup. |
| `bazel_command` | No | `string` | `""` | Override the Bazel binary path. When empty, Tango automatically downloads and caches [Bazelisk](https://github.com/bazelbuild/bazelisk) from GitHub. This lets Tango run without Bazel pre-installed and allows each repository to control its own Bazel version via `.bazelversion`. Set this when the repository requires a custom Bazel wrapper script. |
| `bazel_extra_args` | No | `[]string` | `[]` | Extra arguments passed to `bazel query` invocations. These are inserted between the `query` subcommand and the query expression. Use for query flags like `--profile=/tmp/bazel-profile.json` to capture Bazel trace info for performance debugging.|
| `query_timeout_seconds` | No | `int64` | `600` | Bazel query timeout in seconds. Large monorepos with deep dependency trees may need a higher value. If queries are timing out, increase this. Setting it too low causes premature failures on valid but slow queries. |

## `storage` (optional)

Configures the built-in `memory` or `disk` storage backend. Omit this section if you provide a custom storage implementation.

```yaml
# storage:
#   type: "disk"
#   disk:
#     root_path: "/var/tango/blobs"
```

| Field | Required | Type | Default | Description |
|---|---|---|---|---|
| `type` | No | `string` | `"memory"` | Storage backend. `"memory"` stores blobs in-process — fast but lost on restart, suitable for development and testing. `"disk"` persists blobs to the filesystem so cached graphs survive restarts. |
| `disk.root_path` | Yes (if `type` is `"disk"`) | `string` | | Directory for on-disk blob storage. Must have enough space to hold cached target graphs for all configured repositories. |
