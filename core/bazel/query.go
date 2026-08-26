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
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

func (b *BazelClient) setupCommand(ctx context.Context, query string, startupOptions []string, additionalArgs ...string) commander {
	// Build command: bazel <startupOpts> query <AdditionalArgs> --output=streamed_proto <Query>
	args := make([]string, 0, len(startupOptions)+1+len(additionalArgs)+2)
	args = append(args, startupOptions...)
	args = append(args, "query")
	args = append(args, additionalArgs...)
	args = append(args, "--output=streamed_proto")
	args = append(args, query)
	b.logger.Debug("Querying Bazel", zap.String("workspacePath", b.workspacePath), zap.String("query", query))
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
	var (
		stdoutBuf, stderrBuf bytes.Buffer
		queryResults         *buildpb.QueryResult
	)
	cmdCtx, cancel := context.WithTimeout(ctx, b.queryTimeout)
	defer cancel()
	// setup bazel query command
	cmd := b.setupCommand(cmdCtx, query, startupOptions, additionalArgs...)
	stdout, stdoutWriter := io.Pipe()
	defer func() { _ = stdout.Close() }()
	stderr, stderrWriter := io.Pipe()
	defer func() { _ = stderr.Close() }()
	// In streamLogs mode, stderr goes straight to os.Stderr so operators see
	// bazel progress live; otherwise it's captured for inclusion in failure
	// errors (see wrapQueryFailure).
	stderrSink := io.Writer(&stderrBuf)
	if b.streamLogs {
		stderrSink = os.Stderr
	}
	var readers errgroup.Group
	// Readers must keep draining until Wait returns. If cancellation made them
	// stop early, os/exec's copy goroutines could block writing to these bridge
	// pipes and prevent WaitDelay from completing command cleanup.
	drainCtx := context.WithoutCancel(cmdCtx)
	readers.Go(func() error {
		var err error
		queryResults, err = streamAndParseTargets(drainCtx, stdout, &stdoutBuf)
		return err
	})
	readers.Go(func() error {
		return streamOutput(stderr, stderrSink)
	})

	// Supplying io.Pipe writers makes os/exec own the actual child pipes and
	// their copy goroutines. Wait can therefore run while the readers drain,
	// and WaitDelay can close inherited child descriptors after process exit.
	if err := cmd.Start(stdoutWriter, stderrWriter); err != nil {
		_ = stdoutWriter.Close()
		_ = stderrWriter.Close()
		_ = readers.Wait()
		return nil, err
	}
	waitErr := cmd.Wait()
	// os/exec does not own the bridge writers, so close them after its copy
	// goroutines have stopped, then join both readers before inspecting output.
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	streamErr := readers.Wait()
	if ctxErr := context.Cause(cmdCtx); ctxErr != nil {
		cause := error(ctxErr)
		if waitErr != nil && !errors.Is(waitErr, ctxErr) {
			cause = errors.Join(cause, waitErr)
		}
		if streamErr != nil && !errors.Is(streamErr, ctxErr) {
			cause = errors.Join(cause, streamErr)
		}
		return nil, b.wrapQueryFailure(cmdCtx, "bazel query canceled", cause, &stderrBuf)
	}
	if waitErr != nil {
		return queryResults, b.wrapQueryFailure(cmdCtx, "bazel query failed", waitErr, &stderrBuf)
	}
	if streamErr != nil {
		return nil, b.wrapQueryFailure(cmdCtx, "stream processing failed", streamErr, &stderrBuf)
	}
	b.logger.Debug("Parsed targets from bazel query", zap.Int("target_count", len(queryResults.Target)))
	return queryResults, nil
}

// wrapQueryFailure logs the failure and returns a wrapped error. ctx is the
// query's own timeout context (not a parent cancellation): if it has hit its
// deadline, cause is additionally wrapped with ctx.Err() (context.DeadlineExceeded)
// so callers can identify via errors.Is that this failure was the query's own
// timeout elapsing, as opposed to the caller disconnecting, without a
// dedicated sentinel error. When stderr was captured (streamLogs off), its
// contents are appended so the failure is self-contained. When streamLogs is
// on the operator has already seen stderr live, so it's omitted.
func (b *BazelClient) wrapQueryFailure(ctx context.Context, msg string, cause error, stderrBuf *bytes.Buffer) error {
	if ctxErr := ctx.Err(); ctxErr == context.DeadlineExceeded && !errors.Is(cause, ctxErr) {
		cause = fmt.Errorf("%w: %w", ctxErr, cause)
	}
	tail := ""
	if !b.streamLogs {
		tail = "\nstderr:\n" + stderrBuf.String()
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
	defer func() { _ = f.Close() }()

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
