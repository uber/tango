// Copyright (c) 2025 Uber Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bazel

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

func (b *BazelClient) setupCommand(ctx context.Context, query string, startupOptions []string, additionalArgs ...string) Commander {
	// Build command: bazel <startupOpts> query <AdditionalArgs> --output=streamed_proto <Query>
	args := make([]string, 0, len(startupOptions)+1+len(additionalArgs)+2)
	args = append(args, startupOptions...)
	args = append(args, "query")
	args = append(args, additionalArgs...)
	args = append(args, "--output=streamed_proto")
	args = append(args, query)
	b.logger.Debugw("Querying Bazel", zap.String("workspacePath", b.workspacePath), zap.String("query", query))
	return b.execCommandContext(ctx, b.bazelCommand, args...)
}

func (b *BazelClient) ExecuteQuery(ctx context.Context, req *QueryRequest) (*QueryResponse, error) {
	result, err := b.executeQueryInternal(ctx, req.Query, req.StartupOptions, req.AdditionalArgs...)
	if err != nil {
		return nil, err
	}
	return &QueryResponse{Result: result}, nil
}

func (b *BazelClient) executeQueryInternal(ctx context.Context, query string, startupOptions []string, additionalArgs ...string) (*buildpb.QueryResult, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, b.queryTimeout)
	defer cancel()
	if err := cmdCtx.Err(); err != nil {
		return nil, err
	}
	cmd := b.setupCommand(cmdCtx, query, startupOptions, additionalArgs...)

	// Keep command output backed by files rather than StdoutPipe, StderrPipe,
	// or custom pipe writers. A Bazel descendant can inherit a pipe's write end
	// and prevent readers from seeing EOF after Bazel exits. Calling Wait early
	// to activate its pipe cleanup is not an alternative: Wait may close the
	// read ends before they are fully drained, truncating output. Direct files
	// avoid both lifecycle problems and are rewound for parsing below.
	stdout, err := os.CreateTemp(b.tempDir, "tango-bazel-stdout-*")
	if err != nil {
		return nil, fmt.Errorf("create stdout file: %w", err)
	}
	defer os.Remove(stdout.Name())
	defer stdout.Close()

	// In streamLogs mode, stderr goes straight to os.Stderr so operators see
	// bazel progress live; otherwise it's captured for inclusion in failure
	// errors (see wrapQueryFailure).
	stderrSink := io.Writer(os.Stderr)
	stderrPath := ""
	var stderr *os.File
	if !b.streamLogs {
		stderr, err = os.CreateTemp(b.tempDir, "tango-bazel-stderr-*")
		if err != nil {
			return nil, fmt.Errorf("create stderr file: %w", err)
		}
		defer os.Remove(stderr.Name())
		defer stderr.Close()
		stderrSink = stderr
		stderrPath = stderr.Name()
	}

	// CommandContext and WaitDelay still govern process shutdown; using files
	// changes only output transport and does not weaken cancellation.
	waitErr := cmd.Run(stdout, stderrSink)
	if ctxErr := cmdCtx.Err(); waitErr != nil && ctxErr != nil {
		// A process terminated by SIGTERM or SIGKILL reports an ExitError, which
		// does not wrap the context error. Preserve both causes so callers can
		// reliably identify cancellation, and avoid parsing output after it.
		return nil, b.wrapQueryFailure("bazel query canceled", errors.Join(ctxErr, waitErr), "")
	}

	if _, err := stdout.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind bazel query output: %w", err)
	}
	queryResults, streamErr := getQueryResult(cmdCtx, stdout)
	if waitErr != nil {
		return queryResults, b.wrapQueryFailure("bazel query failed", waitErr, stderrPath)
	}
	if streamErr != nil {
		if cmdCtx.Err() != nil {
			stderrPath = ""
		}
		return nil, b.wrapQueryFailure("stream processing failed", streamErr, stderrPath)
	}
	b.logger.Debugw("Parsed targets from bazel query", zap.Int("target_count", len(queryResults.Target)))
	return queryResults, nil
}

// wrapQueryFailure logs the failure and returns a wrapped error. When stderr
// was captured (streamLogs off), its contents are appended so the failure is
// self-contained. When streamLogs is on the operator has already seen stderr
// live, so it's omitted.
func (b *BazelClient) wrapQueryFailure(msg string, cause error, stderrPath string) error {
	tail := ""
	if stderrPath != "" {
		stderr, err := os.ReadFile(stderrPath)
		if err != nil {
			return fmt.Errorf("%s: %w (read stderr: %v)", msg, cause, err)
		}
		tail = "\nstderr:\n" + string(stderr)
	}
	return fmt.Errorf("%s: %w%s", msg, cause, tail)
}

// FromFile reads a proto file generated by bazel query.
func FromFile(path string) (*buildpb.QueryResult, error) {
	var f io.ReadCloser
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if strings.HasSuffix(path, ".gz") {
		f, err = gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
	}

	out, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	var qr buildpb.QueryResult
	err = proto.Unmarshal(out, &qr)
	if err != nil {
		return nil, err
	}
	return &qr, nil
}
