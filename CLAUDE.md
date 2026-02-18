# Tango OSS - Claude Code Guide

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Tango OSS (Target Analyzer in Go) is a standalone library for fetching and comparing Bazel target graphs. It identifies changed targets between revisions by analyzing:
- Target dependencies
- Build attributes
- Source file content hashes

## Build & Test Commands

### Basic Commands
- **Build all targets**: `make build` or `./tools/bazel build //...`
- **Run all tests**: `make test` or `./tools/bazel test //...`
- **Run a specific test**: `./tools/bazel test //core/workspace:workspace_test`
- **Run a single test function**: `./tools/bazel test //core/workspace:workspace_test --test_filter=TestWorkspaceCheckout`
- **Update BUILD.bazel files**: `make gazelle` (run after adding new Go files or dependencies)
- **Clean build artifacts**: `make clean`

### Running Server & Client
- **Start server**: `make run-server` (listens on port 8081)
- **Run client**: `make run-client` (connects to 127.0.0.1:8081)
- **Client with parameters**: `make run-client REMOTE=mobile/android BASE_SHA=abc123 REQUEST_URLS=https://github.com/uber/repo/pull/123`

### Dependencies & Code Generation
- **Add Go dependency**: Add to `MODULE.bazel` use_repo section, then run `bazel mod tidy`
- **Generate protobuf files**: `make proto` (requires protoc and plugins installed locally)
- **Generate mocks**: `mockgen -package=<pkg>mock -destination=<pkg>mock/<pkg>mock.go . Interface1,Interface2`

## Architecture

### Three-Tier Design
1. **Controller** (`core/controller/`) - YARPC/gRPC service handlers that validate requests
2. **Orchestrator** (`orchestrator/`) - High-level workflow coordination (workspace management, graph computation)
3. **Core Services** - Specialized modules handling specific concerns

### Core Modules
- **bazel/** - Low-level Bazel command execution (query, aquery, cquery)
- **bazelrunner/** - Graph computation using Bazel query APIs
- **git/** - Git operations (checkout, diff, file hashing)
- **storage/** - Persistence layer for target graphs (GraphReader/GraphWriter interfaces)
- **workspace/** - Workspace lifecycle management (checkout, apply changes, release)
- **targethasher/** - Target hashing algorithm for change detection
- **repomanager/** - Repository and workspace leasing
- **config/** - YAML configuration parsing (exclusions, strategies)

### Key Data Flow
```
GetTargetGraph Request
  → Controller validates & delegates
  → Orchestrator leases workspace
  → Checkout base SHA + apply PRs (workspace + git)
  → Execute Bazel query (bazelrunner + bazel)
  → Hash targets (targethasher + git file hashes)
  → Store/return graph (storage)
  → Release workspace
```

### Important Patterns

#### Dependency Injection

All components use `uber-go/fx` with constructor injection:

<table>
<thead><tr><th>Bad</th><th>Good</th></tr></thead>
<tbody>
<tr><td>

```go
func NewOrchestrator(
    bk buildkite.Client,
    st storage.Storage,
) Orchestrator {
    return &orchestrator{
        buildkite: bk,
        storage:   st,
    }
}
```

</td><td>

```go
type Params struct {
    fx.In

    BuildkiteClient buildkite.Client
    Storage         storage.Storage
}

func NewOrchestrator(p Params) Orchestrator {
    return &orchestrator{
        buildkite: p.BuildkiteClient,
        storage:   p.Storage,
    }
}
```

</td></tr>
<tr><td>

Manual parameter wiring is error-prone and doesn't scale with many dependencies.

</td><td>

Params struct enables fx dependency injection and makes dependencies explicit.

</td></tr>
</tbody></table>

#### Interface Compliance Verification

Verify interface implementation at compile time:

```go
var _ storage.Storage = (*Client)(nil)
var _ buildkite.Client = (*client)(nil)
var _ workspace.Workspace = (*workspace)(nil)
```

This ensures breaking changes to interfaces are caught immediately during compilation.

#### Context Propagation

All operations accept `context.Context` as the first parameter:

<table>
<thead><tr><th>Bad</th><th>Good</th></tr></thead>
<tbody>
<tr><td>

```go
func (o *orchestrator) GetGraph(
    req *Request,
) (*Graph, error) {
    // No way to cancel or timeout
    return o.compute(req)
}
```

</td><td>

```go
func (o *orchestrator) GetGraph(
    ctx context.Context,
    req *Request,
) (*Graph, error) {
    // Respects cancellation and deadlines
    return o.compute(ctx, req)
}
```

</td></tr>
<tr><td>

Operations cannot be cancelled or timed out.

</td><td>

Context enables cancellation, timeouts, and request-scoped values.

</td></tr>
</tbody></table>

#### Streaming Responses

gRPC/YARPC use streaming for large graphs to avoid memory exhaustion:

```go
// Stream targets incrementally
for target := range targets {
    if err := stream.Send(target); err != nil {
        return err
    }
}

// Send metadata as final message
return stream.Send(metadata)
```

## Testing

### Test Organization

- Test files follow `*_test.go` naming convention
- Mock packages use `*mock` suffix (e.g., `workspacemock`, `bazelmock`)
- Mocks generated with `uber-go/mock` (mockgen)

### Assertion Libraries

Use `testify` for all assertions:

<table>
<thead><tr><th>Bad</th><th>Good</th></tr></thead>
<tbody>
<tr><td>

```go
if result == nil {
    t.Fatal("expected result")
}
if result.Count != 5 {
    t.Errorf("got %d, want 5", result.Count)
}
```

</td><td>

```go
require.NotNil(t, result)
assert.Equal(t, 5, result.Count)
```

</td></tr>
<tr><td>

Verbose and lacks descriptive failure messages.

</td><td>

Concise with clear assertion intent and helpful failure output.

</td></tr>
</tbody></table>

### Resource Cleanup

Always verify cleanup in defer blocks to prevent resource leaks:

<table>
<thead><tr><th>Bad</th><th>Good</th></tr></thead>
<tbody>
<tr><td>

```go
func TestWorkspace(t *testing.T) {
    ws := setupWorkspace(t)
    result := ws.Checkout(ctx, sha)
    assert.NoError(t, result)
    // Workspace not cleaned up
}
```

</td><td>

```go
func TestWorkspace(t *testing.T) {
    ws := setupWorkspace(t)
    defer func() {
        assert.NoError(t, ws.Release(ctx))
    }()

    result := ws.Checkout(ctx, sha)
    assert.NoError(t, result)
}
```

</td></tr>
<tr><td>

Resources leaked on test completion.

</td><td>

Cleanup happens regardless of test outcome.

</td></tr>
</tbody></table>

### Mock Expectations

Use gomock for interface mocking:

```go
func TestGetTargetGraph(t *testing.T) {
    ctrl := gomock.NewController(t)
    defer ctrl.Finish()

    mockBK := buildkitemock.NewMockClient(ctrl)
    mockSt := storagemock.NewMockStorage(ctrl)

    // Setup expectations
    mockBK.EXPECT().
        CreateBuild(gomock.Any(), gomock.Any()).
        Return(&buildkite.Build{ID: "123"}, nil)

    // Execute
    orch := NewOrchestrator(Params{
        BuildkiteClient: mockBK,
        Storage:         mockSt,
    })

    _, err := orch.GetTargetGraph(ctx, req)
    require.NoError(t, err)
}
```

## Protocol Buffers

### File Organization

- Proto definitions: `proto/tango.proto`
- Generated code: `tangopb/` (checked into version control)
- Three outputs: gogoslick messages, gRPC services, YARPC services

### Regenerating Protos

After modifying proto files:

```bash
# Clean old generated files
make clean-proto

# Regenerate
make proto

# Update mocks if service interfaces changed
mockgen -package=controllermock \
    -destination=core/controller/controllermock/controller_mock.go \
    github.com/uber/tango/tangopb TangoYARPCServer
```

### Proto Best Practices

<table>
<thead><tr><th>Bad</th><th>Good</th></tr></thead>
<tbody>
<tr><td>

```protobuf
message Request {
    string remote = 1;
    string sha = 2;
    repeated string urls = 3;
}
```

</td><td>

```protobuf
message BuildDescription {
    // Git remote URL
    string remote = 1;

    // Base commit SHA
    string base_sha = 2;

    // Pull request URLs to apply
    repeated string request_urls = 3;
}
```

</td></tr>
<tr><td>

Unclear field semantics without comments.

</td><td>

Documented fields explain purpose and format.

</td></tr>
</tbody></table>

## Configuration

### Config File Location

Repository-specific configuration in YAML format:

```bash
# Example config location
example/tango-config.yaml
```

### Config Controls

The YAML configuration controls:
- **Exclusions**: Files and targets to ignore during analysis
- **External targets**: How to handle external dependencies
- **Computation strategy**: NATIVE (direct) or SHELL (subprocess) execution

### Config Timing

<table>
<thead><tr><th>Bad</th><th>Good</th></tr></thead>
<tbody>
<tr><td>

```go
func init() {
    // Load config at startup
    cfg = loadConfig()
}
```

</td><td>

```go
func (o *orchestrator) GetGraph(
    ctx context.Context,
    req *Request,
) (*Graph, error) {
    // Load config per request
    cfg := o.loadConfig(ctx, req.Remote)
    // ...
}
```

</td></tr>
<tr><td>

Static config prevents per-repository customization.

</td><td>

Dynamic config loading enables repository-specific behavior.

</td></tr>
</tbody></table>

## Module Versioning

### Publishing New Versions

Use strict semantic versioning (semver):

```bash
# Tag the release
git tag v0.1.0

# Commit with message
git commit -m "Release v0.1.0"

# Push tag to remote
git push origin v0.1.0
```

### Version Scheme

Follow semver strictly:
- `v0.x.x` - Initial development, breaking changes allowed
- `v1.x.x` - Stable API, backwards compatibility required
- Major version bump - Breaking API changes
- Minor version bump - New features, backwards compatible
- Patch version bump - Bug fixes only

## Error Handling

### Error Context

Always add context to errors:

<table>
<thead><tr><th>Bad</th><th>Good</th></tr></thead>
<tbody>
<tr><td>

```go
func (c *client) GetBuild(
    ctx context.Context,
    id string,
) (*Build, error) {
    resp, err := c.http.Get(url)
    if err != nil {
        return nil, err
    }
    // ...
}
```

</td><td>

```go
func (c *client) GetBuild(
    ctx context.Context,
    id string,
) (*Build, error) {
    resp, err := c.http.Get(url)
    if err != nil {
        return nil, fmt.Errorf(
            "failed to get build %s: %w",
            id, err,
        )
    }
    // ...
}
```

</td></tr>
<tr><td>

Raw errors lack context about the operation.

</td><td>

Wrapped errors provide debugging context while preserving the original error.

</td></tr>
</tbody></table>

### Sentinel Errors

Use sentinel errors for well-known error conditions:

```go
package storage

import "errors"

var (
    // ErrNotFound is returned when a graph is not found in storage
    ErrNotFound = errors.New("graph not found")

    // ErrInvalidKey is returned for malformed storage keys
    ErrInvalidKey = errors.New("invalid storage key")
)

// Check for specific errors using errors.Is
if errors.Is(err, storage.ErrNotFound) {
    // Handle not found case
}
```

### Error Types

Use custom error types for rich error information:

```go
type ValidationError struct {
    Field string
    Value interface{}
    Err   error
}

func (e *ValidationError) Error() string {
    return fmt.Sprintf(
        "validation failed for %s=%v: %v",
        e.Field, e.Value, e.Err,
    )
}

func (e *ValidationError) Unwrap() error {
    return e.Err
}
```

## Common Patterns

### Zero-Value Mutexes

The zero-value of `sync.Mutex` and `sync.RWMutex` is valid:

<table>
<thead><tr><th>Bad</th><th>Good</th></tr></thead>
<tbody>
<tr><td>

```go
type Client struct {
    mu *sync.Mutex
    data map[string]string
}

func NewClient() *Client {
    return &Client{
        mu:   new(sync.Mutex),
        data: make(map[string]string),
    }
}
```

</td><td>

```go
type Client struct {
    mu   sync.Mutex
    data map[string]string
}

func NewClient() *Client {
    return &Client{
        data: make(map[string]string),
    }
}
```

</td></tr>
<tr><td>

Unnecessary allocation and pointer indirection.

</td><td>

Zero-value mutex works correctly and keeps mutex unexported.

</td></tr>
</tbody></table>

### Copying Slices and Maps

Copy at boundaries to prevent unintended mutations:

<table>
<thead><tr><th>Bad</th><th>Good</th></tr></thead>
<tbody>
<tr><td>

```go
type Orchestrator struct {
    targets []string
}

func (o *Orchestrator) GetTargets() []string {
    return o.targets
}
```

</td><td>

```go
type Orchestrator struct {
    targets []string
}

func (o *Orchestrator) GetTargets() []string {
    result := make([]string, len(o.targets))
    copy(result, o.targets)
    return result
}
```

</td></tr>
<tr><td>

Caller can mutate internal state.

</td><td>

Returns a copy, preventing unintended modifications.

</td></tr>
</tbody></table>

### Defer for Cleanup

Use defer for cleanup operations:

<table>
<thead><tr><th>Bad</th><th>Good</th></tr></thead>
<tbody>
<tr><td>

```go
func (w *workspace) Process() error {
    w.mu.Lock()

    if err := w.validate(); err != nil {
        w.mu.Unlock()
        return err
    }

    if err := w.execute(); err != nil {
        w.mu.Unlock()
        return err
    }

    w.mu.Unlock()
    return nil
}
```

</td><td>

```go
func (w *workspace) Process() error {
    w.mu.Lock()
    defer w.mu.Unlock()

    if err := w.validate(); err != nil {
        return err
    }

    if err := w.execute(); err != nil {
        return err
    }

    return nil
}
```

</td></tr>
<tr><td>

Manual unlock is error-prone and repetitive.

</td><td>

Defer ensures cleanup happens regardless of return path.

</td></tr>
</tbody></table>

## Imports

### Import Grouping

Organize imports in three groups separated by blank lines:

```go
import (
    // Standard library
    "context"
    "fmt"
    "time"

    // Third-party packages
    "github.com/stretchr/testify/assert"
    "go.uber.org/zap"

    // Local packages
    "github.com/uber/tango/core/storage"
    "github.com/uber/tango/tangopb"
)
```

Use `goimports` to automatically organize imports correctly.

## Linting

### Required Tools

Configure your editor to run these tools on save:
- `goimports` - Automatically manage imports and formatting
- `golint` - Check for common style mistakes
- `go vet` - Detect suspicious constructs

### Running Linters

```bash
# Format all code
goimports -w .

# Run standard checks
go vet ./...

# Run golint
golint ./...
```
