# Tango OSS
Tango OSS (Target Analyzer in Go) provides APIs for fetching and comparing target graphs and services. It is a standalone library that can be used and executed independently of the monorepo.

## Quick Start

### Prerequisites
- Bazel 8.4.1 (automatically managed via `tools/bazel`)
- protoc (for proto generation only)
- direnv (optional, for loading `.envrc`)

### Build and Test

The project includes a Makefile for common tasks:

```bash
# Build all targets
make build

# Run all tests
make test

# Build and test
make build && make test

# Show all available commands
make help
```

### Run Server & Client

```bash
# Run the Tango server (port 8081)
make run-server

# In another terminal, run the client
make run-client

# Run client with custom parameters
make run-client REMOTE=mobile/android BASE_SHA=abc123 REQUEST_URLS=https://github.com/uber/repo/pull/123
```

For a complete list of available commands, run `make help`.

## Installation

The project is self-contained and uses `tools/bazel` wrapper to automatically download and manage Bazel.

**Optional: Set up direnv**
```bash
# Install direnv (macOS)
brew install direnv

# Add to your shell (bash/zsh)
echo 'eval "$(direnv hook bash)"' >> ~/.bashrc  # or ~/.zshrc

# Allow .envrc in the project
direnv allow
```

Once set up, you can build the project:
```bash
make build
# or directly with the bazel wrapper
./tools/bazel build //...
```

## Development

### Updating BUILD files
- Add all direct GO dependencies explicitly to the MODULE.bazel.
- Update BUILD files by running `make gazelle` (or `bazel run //:gazelle`)
- If an external dependency is added, run `bazel mod tidy` to add the dependency to the repo

### Generating Protobuf Files

Install protoc locally: https://github.com/protocolbuffers/protobuf?tab=readme-ov-file#protobuf-compiler-installation

Install required protoc plugins:
```bash
go install github.com/gogo/protobuf/protoc-gen-gogoslick@latest
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
go install go.uber.org/yarpc/encoding/protobuf/protoc-gen-yarpc-go@latest
```

Generate protobuf files (generates gogoslick, grpc, and yarpc):
```bash
make proto

# To regenerate from scratch
make clean-proto && make proto
```

This generates:
- `tangopb/tango.pb.go` - Protocol buffer messages (gogoslick)
- `tangopb/tango_grpc.pb.go` - gRPC service definitions
- `tangopb/tango.pb.yarpc.go` - YARPC service definitions

**Note:** Generated proto files are checked into version control. The `clean-proto` command is normally not needed unless you want to regenerate from scratch.

### Generating mocks with mockgen
To install mockgen, run `go install go.uber.org/mock/mockgen@latest`.
To regenerate mocks for a package run
```
mockgen -destination=<source file> <package path> <Interface><Interface>
# Run in the current package
mockgen -destination=<source file> . <Interface><Interface>
# Example
mockgen -package=tangopbmock  -self_package=tangopbmock  -destination=tangopbmock/tangopbmock.go . TangoServiceGetChangedServicesYARPCServer,TangoServiceGetChangedTargetGraphYARPCServer,TangoServiceGetChangedTargetsYARPCServer,TangoServiceGetTargetGraphYARPCServer
```

### Update new module version
Run
```bash
git tag <version>          # git tag 3.0.27.35
git commit -m "Initialize new version"
git push origin <version>  # git push origin 3.0.27.35
```

## Available Make Commands

Run `make help` to see all available commands:

**Build & Test:**
- `make build` - Build all targets
- `make test` - Run all tests
- `make gazelle` - Update BUILD.bazel files
- `make clean` - Clean generated files and binaries

**Protobuf:**
- `make proto` - Generate protobuf files (gogoslick, grpc, yarpc)
- `make clean-proto` - Clean generated proto files

**Run Server & Client:**
- `make run-server` - Run the Tango server (port 8081)
- `make run-client` - Run the Tango client

**Other:**
- `make version` - Show Bazel version
- `make help` - Show this help message

**Client Parameters:**
You can customize the client behavior with these environment variables:
- `SERVER_ADDR` - Server address (default: 127.0.0.1:8081)
- `METHOD` - RPC method to call (default: get-target-graph)
- `REMOTE` - Build description remote
- `BASE_SHA` - Build description base SHA
- `REQUEST_URLS` - Comma-separated change request URLs

Example:
```bash
make run-client REMOTE=mobile/android BASE_SHA=abc123 REQUEST_URLS=https://github.com/uber/repo/pull/123
```

## CI/CD

This project uses GitHub Actions for continuous integration. The workflow automatically:
- Builds all targets
- Runs all tests
- Reports test failures with detailed logs

The workflow runs on:
- All pushes to the `main` branch
- All pull requests (opened, reopened, or synchronized)
