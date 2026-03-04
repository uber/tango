# Contributing to Tango

We welcome contributions from the community. This document describes how to
get started.

## Getting Started

1. Fork the repository on GitHub.
2. Clone your fork locally: `git clone https://github.com/<your-user>/tango.git`
3. Create a feature branch: `git checkout -b my-feature`
4. Make your changes and ensure tests pass: `make build && make test`
5. Test behavior: `make run-server` and `make run-client-get-graph REMOTE=org/repo BASE_SHA=abc123 REQUEST_URLS=https://github.com/uber/repo/pull/123` (See [README](README.md) for more details)
6. Commit your changes with a descriptive message.
7. Push to your fork and open a Pull Request against `main`.

## Development Setup

See the [README](README.md) for prerequisites and build instructions.
After making changes to Go files, run `make gazelle` to update BUILD.bazel
files.

## Pull Request Guidelines

- Keep PRs focused on a single change.
- Include tests for new functionality.
- Ensure all existing tests pass (`bazel test //...`).
- Follow the existing code style and patterns (see `CLAUDE.md` for detailed
  conventions).
- Fill out the PR template with a description, motivation, and test plan.

## Code Review

All submissions require review before merging. We use GitHub pull requests
for this purpose. A maintainer will review your PR and may request changes.

## Reporting Issues

Use [GitHub Issues](https://github.com/uber/tango/issues) to report bugs or
request features. Please check existing issues before creating a new one.

## Code of Conduct

This project adheres to the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md).
By participating, you are expected to uphold this code.

## License

By contributing to Tango, you agree that your contributions will be licensed
under the [Apache License 2.0](LICENSE).
