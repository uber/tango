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
// It runs the same query that nativeGraphRunner uses and logs timing and target counts for each phase.
//
// Usage:
//
//	bazel run //example/cmd/query-bench -- --workspace /path/to/repo
//	bazel run //example/cmd/query-bench -- --workspace /path/to/repo --bazel bazelisk --runs 3
//	bazel run //example/cmd/query-bench -- --workspace /path/to/repo --exclude-external
//	bazel run //example/cmd/query-bench -- --workspace /path/to/repo --query '//...:all-targets'
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/pprof"
	"time"

	"github.com/gogo/protobuf/jsonpb"
	"github.com/uber/tango/config"
	"github.com/uber/tango/core/bazel"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/targethasher"
	"github.com/uber/tango/internal/mapper"
	tgmapper "github.com/uber/tango/mapper"
	"go.uber.org/zap"
)

func main() {
	logger, err := zap.NewDevelopment()
	if err != nil {
		panic(fmt.Sprintf("creating logger: %v", err))
	}
	if err := run(logger); err != nil {
		logger.Error("benchmark failed", zap.Error(err))
		_ = logger.Sync()
		os.Exit(1)
	}
	_ = logger.Sync()
}

func run(logger *zap.Logger) error {
	bazelCmd := flag.String("bazel", "", "bazel binary to invoke (default: auto-detect bazel/bazelisk on PATH)")
	workspace := flag.String("workspace", ".", "workspace root to run bazel query in")
	query := flag.String("query", "", "bazel query expression (default: the standard nativeGraphRunner query)")
	excludeExternal := flag.Bool("exclude-external", false, "use deps(//...:all-targets) instead of including //external:all-targets")
	bzlmod := flag.Bool("bzlmod", true, "mirror nativeGraphRunner for a Bzlmod repo: drop //external from the query, read repo marker hashes, hash with UseBzlmod")
	cpuProfile := flag.String("cpuprofile", "", "write a CPU profile of the targethasher phase to this file")
	dump := flag.Bool("dump", false, "write the resulting graph chunks to stdout as JSON")
	runs := flag.Int("runs", 1, "number of times to run the query (for benchmarking)")
	timeout := flag.Duration("timeout", 30*time.Minute, "per-run timeout")
	flag.Parse()

	q := *query
	if q == "" {
		if *excludeExternal || *bzlmod {
			q = "deps(//...:all-targets)"
		} else {
			q = "//external:all-targets + deps(//...:all-targets)"
		}
	}

	parentCtx := context.Background()

	client, err := bazel.NewBazelClient(parentCtx, bazel.Params{
		BazelCommand:  *bazelCmd,
		WorkspacePath: *workspace,
		Logger:        logger,
		QueryTimeout:  *timeout,
	})
	if err != nil {
		return fmt.Errorf("creating bazel client: %w", err)
	}

	req := &bazel.QueryRequest{
		Query:          q,
		AdditionalArgs: []string{"--order_output=no", "--proto:locations", "--noproto:default_values"},
	}

	logger.Info("starting benchmark",
		zap.String("workspace", *workspace),
		zap.String("query", q),
		zap.Int("runs", *runs))

	var totalDuration time.Duration
	for i := range *runs {
		ctx, cancel := context.WithTimeout(parentCtx, *timeout)
		start := time.Now()
		resp, err := client.ExecuteQuery(ctx, req)
		elapsed := time.Since(start)
		defer cancel()

		if err != nil {
			return fmt.Errorf("run %d: query failed: %w", i+1, err)
		}

		totalDuration += elapsed
		logger.Info("bazel query",
			zap.Int("run", i+1),
			zap.Duration("duration", elapsed.Round(time.Millisecond)),
			zap.Int("targets", len(resp.Result.Target)))

		hashConfig := targethasher.HashConfig{UseBzlmod: *bzlmod}
		if *bzlmod {
			start = time.Now()
			hashConfig.KnownSourceHashes, err = git.New(*workspace, logger).FileHashes(ctx, "HEAD")
			if err != nil {
				return fmt.Errorf("run %d: git file hashes: %w", i+1, err)
			}
			elapsed = time.Since(start)
			totalDuration += elapsed
			logger.Info("git file hashes",
				zap.Int("run", i+1),
				zap.Duration("duration", elapsed.Round(time.Millisecond)),
				zap.Int("files", len(hashConfig.KnownSourceHashes)))

			start = time.Now()
			hashConfig.RepoMarkerHashes, err = targethasher.ReadRepoMarkerHashes(ctx, *workspace, *bazelCmd)
			if err != nil {
				return fmt.Errorf("run %d: read repo marker hashes: %w", i+1, err)
			}
			elapsed = time.Since(start)
			totalDuration += elapsed
			logger.Info("marker read",
				zap.Int("run", i+1),
				zap.Duration("duration", elapsed.Round(time.Millisecond)),
				zap.Int("repos", len(hashConfig.RepoMarkerHashes)))

			start = time.Now()
			hashConfig.RepoMapping, err = bazel.RepoMapping(ctx, *workspace, *bazelCmd)
			if err != nil {
				return fmt.Errorf("run %d: read repo mapping: %w", i+1, err)
			}
			elapsed = time.Since(start)
			totalDuration += elapsed
			logger.Info("repo mapping",
				zap.Int("run", i+1),
				zap.Duration("duration", elapsed.Round(time.Millisecond)),
				zap.Int("repos", len(hashConfig.RepoMapping)))
		}

		start = time.Now()
		stopProfile, err := startCPUProfile(*cpuProfile)
		if err != nil {
			return err
		}
		targethasherResult, err := targethasher.FromProto(ctx, resp.Result, *workspace, hashConfig)
		stopProfile()
		if err != nil {
			return fmt.Errorf("converting result to targethasher.Result: %w", err)
		}
		elapsed = time.Since(start)
		totalDuration += elapsed
		logger.Info("target hasher",
			zap.Int("run", i+1),
			zap.Duration("duration", elapsed.Round(time.Millisecond)),
			zap.Int("targets", len(targethasherResult.TargetNames)))
		start = time.Now()
		chunks, err := tgmapper.ResultToGraphChunks(ctx, targethasherResult, config.DefaultMaxMessageBytes)
		if err != nil {
			return fmt.Errorf("run %d: converting to graph chunks: %w", i+1, err)
		}
		elapsed = time.Since(start)
		logger.Info("result to graph chunks",
			zap.Int("run", i+1),
			zap.Duration("duration", elapsed.Round(time.Millisecond)),
			zap.Int("chunks", len(chunks)))
		if !*dump {
			continue
		}
		m := jsonpb.Marshaler{Indent: "  "}
		for _, chunk := range chunks {
			protoResp := mapper.GetTargetGraphResponseToProto(&chunk)
			if err := m.Marshal(os.Stdout, protoResp); err != nil {
				return fmt.Errorf("run %d: encoding response: %w", i+1, err)
			}
			if _, err := io.WriteString(os.Stdout, "\n"); err != nil {
				return fmt.Errorf("run %d: writing response: %w", i+1, err)
			}
		}
	}

	if *runs > 1 {
		logger.Info("average compute duration",
			zap.Duration("duration", (totalDuration/time.Duration(*runs)).Round(time.Millisecond)),
			zap.Int("runs", *runs))
	}
	return nil
}

// startCPUProfile starts a CPU profile written to path and returns a func that stops it. An empty path is a no-op.
func startCPUProfile(path string) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("creating cpu profile: %w", err)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("starting cpu profile: %w", err)
	}
	return func() {
		pprof.StopCPUProfile()
		f.Close()
	}, nil
}
