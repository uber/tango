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

// query-bench is a local benchmarking tool for the bazel query execution path and targethasher.
// It runs the same query that nativeGraphRunner uses and reports timing and target counts.
//
// Usage:
//
//	bazel run //cmd/query-bench -- --workspace /path/to/repo
//	bazel run //cmd/query-bench -- --workspace /path/to/repo --bazel bazelisk --runs 3
//	bazel run //cmd/query-bench -- --workspace /path/to/repo --exclude-external
//	bazel run //cmd/query-bench -- --workspace /path/to/repo --query '//...:all-targets'
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/uber/tango/core/bazel"
	"github.com/uber/tango/core/targethasher"
	"go.uber.org/zap"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	bazelCmd := flag.String("bazel", "", "bazel binary to invoke (default: auto-detect bazel/bazelisk on PATH)")
	workspace := flag.String("workspace", ".", "workspace root to run bazel query in")
	query := flag.String("query", "", "bazel query expression (default: the standard nativeGraphRunner query)")
	excludeExternal := flag.Bool("exclude-external", false, "use deps(//...:all-targets) instead of including //external:all-targets")
	runs := flag.Int("runs", 1, "number of times to run the query (for benchmarking)")
	timeout := flag.Duration("timeout", 30*time.Minute, "per-run timeout")
	flag.Parse()

	q := *query
	if q == "" {
		if *excludeExternal {
			q = "deps(//...:all-targets)"
		} else {
			q = "//external:all-targets + deps(//...:all-targets)"
		}
	}

	logger, err := zap.NewDevelopment()
	if err != nil {
		return fmt.Errorf("creating logger: %w", err)
	}
	defer logger.Sync()

	client, err := bazel.NewBazelClient(bazel.Params{
		BazelCommand:  *bazelCmd,
		WorkspacePath: *workspace,
		Logger:        logger.Sugar(),
		QueryTimeout:  *timeout,
	})
	if err != nil {
		return fmt.Errorf("creating bazel client: %w", err)
	}

	req := &bazel.QueryRequest{
		Query:          q,
		AdditionalArgs: []string{"--order_output=no", "--proto:locations", "--noproto:default_values"},
	}

	fmt.Printf("workspace:  %s\n", *workspace)
	fmt.Printf("query:      %s\n", q)
	fmt.Printf("runs:       %d\n\n", *runs)

	var totalDuration time.Duration
	for i := range *runs {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		start := time.Now()
		resp, err := client.ExecuteQuery(ctx, req)
		elapsed := time.Since(start)
		defer cancel()

		if err != nil {
			return fmt.Errorf("run %d: query failed: %w", i+1, err)
		}

		totalDuration += elapsed
		fmt.Printf("run %d: bazel query: %v  (%d targets)\n", i+1, elapsed.Round(time.Millisecond), len(resp.Result.Target))
		start = time.Now()
		targethasherResult, err := targethasher.FromProto(ctx, resp.Result, *workspace, targethasher.HashConfig{})
		if err != nil {
			return fmt.Errorf("converting result to targethasher.Result: %w", err)
		}
		elapsed = time.Since(start)
		totalDuration += elapsed
		fmt.Printf("run %d: targethasher: %v  (%d targets)\n", i+1, elapsed.Round(time.Millisecond), len(targethasherResult.TargetNames))
	}

	if *runs > 1 {
		fmt.Printf("\naverage: %v\n", (totalDuration / time.Duration(*runs)).Round(time.Millisecond))
	}
	return nil
}
