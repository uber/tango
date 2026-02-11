.PHONY: build test proto gazelle clean clean-proto run-server run-client help

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

# Generate protobuf files using protoc
proto:
	@echo "Generating protobuf files with protoc..."
	@protoc --go_out=tangopb --go_opt=paths=source_relative \
	  --go-grpc_out=tangopb --go-grpc_opt=paths=source_relative \
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

# Run the Tango client
run-client:
	@$(BAZEL) run //example/client:client -- \
		-addr $(or $(SERVER_ADDR),127.0.0.1:8081) \
		-method $(or $(METHOD),get-target-graph) \
		-remote "$(or $(REMOTE),)" \
		-base-sha "$(or $(BASE_SHA),)" \
		-request-urls "$(or $(REQUEST_URLS),)"

# Show Bazel version
version:
	@$(BAZEL) version

# Help
help:
	@echo "Available targets:"
	@echo ""
	@echo "Build & Test:"
	@echo "  make build         - Build all targets"
	@echo "  make test          - Run all tests"
	@echo "  make gazelle       - Update BUILD.bazel files"
	@echo "  make clean         - Clean generated files and binaries"
	@echo ""
	@echo "Protobuf:"
	@echo "  make proto         - Generate protobuf files (go, grpc, yarpc)"
	@echo "  make clean-proto   - Clean generated proto files"
	@echo ""
	@echo "Run Server & Client:"
	@echo "  make run-server    - Run the Tango server (port 8081)"
	@echo "  make run-client    - Run the Tango client"
	@echo ""
	@echo "Other:"
	@echo "  make version       - Show Bazel version"
	@echo "  make help          - Show this help message"
	@echo ""
	@echo "Examples:"git
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
	@echo "  make run-client REMOTE=mobile/android BASE_SHA=abc123"
	@echo ""
