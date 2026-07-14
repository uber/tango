# Tango Configuration

Tango is configured via a single YAML file. Pass the file path to `config.Parse` at startup.

## Configuration Lifecycle

### `service` / `storage` — requires a deployment/restart

Editing the YAML file while the service is running does nothing until it's deployed or restarted. To pick up a change to `service` or `storage`, redeploy the service.

### `repository`

`repository` config is read per request via `RepositoryConfigProvider`, not baked into a constructor at startup, so it doesn't have to come from the static file at all. It could be fetched from a third-party config service that Tango fetches per request, so a new repo or a changed setting shows up without a deployment. Or, since Tango clones and checks out the target repo anyway, it could just as easily be a plain file checked into that repo itself and read out of the checkout at request time. Either way, `repository` config can change while the service keeps running.

---

## `service`

Controls how Tango operates at the service level — how many concurrent requests it can handle per repository, where it stores clones and worker checkouts on disk, and how it chunks responses over gRPC streams.

```yaml
service:
  max_worker_pool_size: 5
  workspaces_root_path: "/var/tango"
  max_message_bytes: 4250000
```

| Field | Required | Type | Default | Description |
|---|---|---|---|---|
| `max_worker_pool_size` | **Yes** | `int` | | Max number of concurrent requests per repository. Each worker is a lightweight local clone (hardlinked to the origin, not a full copy) that handles one request at a time — setting this to 5 means up to 5 concurrent graph computations per repo, and additional requests queue until a worker is free. A good starting point is the expected peak concurrent requests per repo. Must be greater than 0. |
| `workspaces_root_path` | **Yes** | `string` | | Root directory where Tango stores repository clones and worker checkouts. Required because Tango needs a known location to clone repositories and create worker checkouts — without it, there is no disk space allocated for graph computation. Choose a location with enough disk space to hold one origin clone plus N worker checkouts (where N is `max_worker_pool_size`) per configured repository. Use a persistent location in production — if this directory is lost, Tango re-clones from the remote on the next request, which can be slow for large repositories. Layout: `<workspaces_root_path>/<repo>/` for origin clones and `<workspaces_root_path>/.workers/<repo>/worker-{1..N}/` for worker checkouts. |
| `max_message_bytes` | No | `int` | `4250000` (~4.25MB) | Caps the size of each gRPC stream message Tango sends (targets, changed targets, and metadata mappings — see `proto/tango.proto`). This is a single byte budget rather than separate per-entry-type counts: Tango derives how many targets, changed targets, or metadata entries fit in one message from fixed size assumptions (a target ≈ 40KB worst case, a changed target ≈ 2x that since it carries both an old and new target, a metadata entry ≈ 85 bytes), computed once rather than measured per request. If a repository has unusually large targets or metadata entries, tune this down to fit its needs. |


## `repository`

A list of repository entries. Each entry tells Tango how to clone, and query for a specific repository — including which Bazel settings to use.

```yaml
repository:
  - remote: "https://github.com/uber/tango.git"
    bzlmod_enabled: true
    query_timeout_seconds: 600
```

| Field | Required | Type | Default | Description |
|---|---|---|---|---|
| `remote` | **Yes** | `string` | | The URL used to `git clone` the repository. Tango clones from this URL and uses it as the lookup key for per-repo settings. Must be unique across all entries and match exactly what clients send in `BuildDescription.remote`. |
| `bzlmod_enabled` | No | `bool` | `true` | Whether this repository uses Bzlmod for external dependency management. Bzlmod is enforced in Bazel 9+. Set to false only for repositories still using WORKSPACE — when disabled, `//external:all-targets` is added to Bazel queries. Setting this incorrectly causes query failures — it must match the repository's actual dependency management setup. |
| `bazel_command_path` | No | `string` | `""` | Override the Bazel binary path. When empty, Tango automatically downloads and caches [Bazelisk](https://github.com/bazelbuild/bazelisk) from GitHub. This lets Tango run without Bazel pre-installed and allows each repository to control its own Bazel version via `.bazelversion`. Set this when the repository requires a custom Bazel wrapper script. |
| `bazel_extra_args` | No | `[]string` | `[]` | Extra arguments passed to `bazel query` invocations. These are inserted between the `query` subcommand and the query expression. Use for query flags like `--profile=/tmp/bazel-profile.json` to capture Bazel trace info for performance debugging.|
| `query_timeout_seconds` | No | `int64` | `600` | Bazel query timeout in seconds. Large monorepos with deep dependency trees may need a higher value. If queries are timing out, increase this. Setting it too low causes premature failures on valid but slow queries. |

## `storage` (optional)

Configures the built-in `memory` or `disk` storage backend. Omit this section if you provide a custom storage implementation.

```yaml
storage:
  type: "disk"
  disk:
    root_path: "/var/tango/blobs"
```

| Field | Required | Type | Default | Description |
|---|---|---|---|---|
| `type` | No | `string` | `"memory"` | Storage backend. `"memory"` stores blobs in-process — fast but lost on restart, suitable for development and testing. `"disk"` persists blobs to the filesystem so cached graphs survive restarts. |
| `disk.root_path` | Yes (if `type` is `"disk"`) | `string` | | Directory for on-disk blob storage. Must have enough space to hold cached target graphs for all configured repositories. |
