.PHONY: build test test-integration proto gazelle clean clean-proto run-server run-client-get-graph run-client-changed-targets help

# Bazel wrapper
BAZEL = ./tools/bazel

# Build everything in the project using Bazel
build:
	@echo "Building all targets with Bazel..."
	@$(BAZEL) build //...
	@echo "Build complete!"

# Run all tests using Bazel
test:
	@echo "Running all tests..."
	@$(BAZEL) test //...
	@echo "All tests passed!"

# Run integration tests (requires bazel; may take several minutes)
test-integration:
	@echo "Running integration tests..."
	@$(BAZEL) test //integration:integration_test --test_output=errors --test_env=TANGO_REPO_REMOTE=$$(git rev-parse --show-toplevel)
	@echo "Integration tests passed!"

# Generate protobuf files using protoc
proto:
	@echo "Generating protobuf files with protoc..."
	@protoc --gogoslick_out=tangopb \
	  --yarpc-go_out=tangopb --yarpc-go_opt=paths=source_relative \
	  --proto_path=proto proto/tango.proto
	@echo "Protobuf files generated successfully!"

# Generate/update BUILD.bazel files using Gazelle
gazelle:
	@echo "Running Gazelle to update BUILD files..."
	@$(BAZEL) run //:gazelle
	@echo "BUILD files updated!"

# Clean generated files and binaries
clean:
	@echo "Cleaning with Bazel..."
	@$(BAZEL) clean
	@echo "Clean complete!"

# Clean generated proto files (normally not needed as they are checked in)
clean-proto:
	@echo "Cleaning generated proto files..."
	@rm -rf tangopb/*.pb.go
	@echo "Proto clean complete!"

# Run the Tango server
run-server:
	@echo "Running Tango server on port 8081..."
	@$(BAZEL) run //example:example

# Run get-target-graph via the Tango client
run-client-get-graph:
	@$(BAZEL) run //example/client:client -- \
		-addr $(or $(SERVER_ADDR),127.0.0.1:8081) \
		-method $(or $(METHOD),get-target-graph) \
		-remote "$(or $(REMOTE),)" \
		-base-sha "$(or $(BASE_SHA),)" \
		-request-urls "$(or $(REQUEST_URLS),)" \
		-include-hashes=$(or $(INCLUDE_HASHES),false) \
		-include-tags=$(or $(INCLUDE_TAGS),false) \
		-include-attributes=$(or $(INCLUDE_ATTRIBUTES),false)

# Run get-changed-targets via the Tango client
run-client-changed-targets:
	@$(BAZEL) run //example/client:client -- \
		-addr $(or $(SERVER_ADDR),127.0.0.1:8081) \
		-method get-changed-targets \
		-remote "$(or $(REMOTE),)" \
		-base-sha "$(or $(BASE_SHA),)" \
		-request-urls "$(or $(REQUEST_URLS),)" \
		-new-base-sha "$(or $(NEW_BASE_SHA),)" \
		-new-request-urls "$(or $(NEW_REQUEST_URLS),)" \
		-include-hashes=$(or $(INCLUDE_HASHES),false) \
		-include-tags=$(or $(INCLUDE_TAGS),false) \
		-include-attributes=$(or $(INCLUDE_ATTRIBUTES),false)

# Show Bazel version
version:
	@$(BAZEL) version

# Help
help:
	@echo "Available targets:"
	@echo ""
	@echo "Build & Test:"
	@echo "  make build            - Build all targets"
	@echo "  make test             - Run all tests"
	@echo "  make test-integration - Run integration tests (slow)"
	@echo "  make gazelle          - Update BUILD.bazel files"
	@echo "  make clean            - Clean generated files and binaries"
	@echo ""
	@echo "Protobuf:"
	@echo "  make proto         - Generate protobuf files (gogoslick, grpc, yarpc)"
	@echo "  make clean-proto   - Clean generated proto files"
	@echo ""
	@echo "Run Server & Client:"
	@echo "  make run-server    - Run the Tango server (port 8081)"
	@echo "  make run-client-get-graph     - Run get-target-graph via the Tango client"
	@echo "  make run-client-changed-targets - Run get-changed-targets via the Tango client"
	@echo ""
	@echo "Other:"
	@echo "  make version       - Show Bazel version"
	@echo "  make help          - Show this help message"
	@echo ""
	@echo "Examples:"
	@echo "  # Build and test everything"
	@echo "  make build && make test"
	@echo ""
	@echo "  # Update BUILD files and build"
	@echo "  make gazelle && make build"
	@echo ""
	@echo "  # Regenerate proto files"
	@echo "  make clean-proto && make proto"
	@echo ""
	@echo "  # Run the Tango server"
	@echo "  make run-server"
	@echo ""
	@echo "  # Run client with custom parameters"
	@echo "  make run-client-get-graph REMOTE=org/repo BASE_SHA=abc123"
	@echo "  # Run get-changed-targets via the Tango client"
	@echo "  make run-client-changed-targets REMOTE=org/repo BASE_SHA=abc123 NEW_BASE_SHA=abc123~"
	@echo "  # Opt into per-target fields (default false per proto3)"
	@echo "  make run-client-changed-targets REMOTE=org/repo BASE_SHA=abc123 NEW_BASE_SHA=abc123~ INCLUDE_TAGS=true INCLUDE_HASHES=true INCLUDE_ATTRIBUTES=true"
