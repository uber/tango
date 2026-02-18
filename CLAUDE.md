# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Tango OSS (Target Analyzer in Go) is a standalone library for fetching and comparing Bazel target graphs. It helps identify changed targets between revisions by analyzing target dependencies, attributes, and source file hashes.

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
- **Dependency Injection**: Uses `uber-go/fx` with `Params` structs for constructor injection
- **Interface-based design**: All core components have interfaces for testability (mocks in `*mock` packages)
- **Context propagation**: All operations accept `context.Context` for cancellation and timeouts
- **Streaming responses**: gRPC/YARPC use streaming for large graph responses

## Testing

- Tests use `testify/assert` and `testify/require` for assertions
- Mocks generated with `uber-go/mock` (mockgen)
- Test files follow `*_test.go` naming convention
- Mock packages use `*mock` suffix (e.g., `workspacemock`, `bazelmock`)
- When testing, verify workspace cleanup in defer blocks to prevent resource leaks

## Protocol Buffers

- Proto definitions in `proto/tango.proto`
- Generated files in `tangopb/` (checked into version control)
- Generates three outputs: gogoslick messages, gRPC services, YARPC services
- When modifying protos: `make clean-proto && make proto`, then update mocks if service interfaces changed

## Configuration

- Repository-specific config in YAML (see `example/tango-config.yaml`)
- Config controls: exclusions (files/targets), external target handling, computation strategy
- Config is parsed at request time, not startup

## Module Versioning

To publish a new version:
```bash
git tag v0.0.1
git commit -m "Release v0.0.1"
git push origin v0.0.1
```
Use strict semantic version for versioning.
