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
	"log"
	"os"
	"runtime/pprof"
	"time"

	"github.com/gogo/protobuf/jsonpb"
	"github.com/uber/tango/config"
	"github.com/uber/tango/core/bazel"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/targethasher"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/mapper"
	tgmapper "github.com/uber/tango/mapper"
	"go.uber.org/zap"
)

type options struct {
	bazelCmd   string
	workspace  string
	query      string
	bzlmod     bool
	cpuProfile string
	dump       bool
	runs       int
	timeout    time.Duration
}

func main() {
	os.Exit(exitCode())
}

// exitCode runs the benchmark and returns the process exit code, so that main is the only caller of os.Exit
// and deferred cleanup still runs.
func exitCode() int {
	logger, err := zap.NewDevelopment()
	if err != nil {
		log.Printf("creating logger: %v", err)
		return 1
	}
	defer func() { _ = logger.Sync() }()

	if err := run(logger); err != nil {
		logger.Error("benchmark failed", zap.Error(err))
		return 1
	}
	return 0
}

func run(logger *zap.Logger) error {
	opts := parseOptions()

	client, err := bazel.NewBazelClient(context.Background(), bazel.Params{
		BazelCommand:  opts.bazelCmd,
		WorkspacePath: opts.workspace,
		Logger:        logger,
		QueryTimeout:  opts.timeout,
	})
	if err != nil {
		return fmt.Errorf("creating bazel client: %w", err)
	}

	req := &bazel.QueryRequest{
		Query:          opts.query,
		AdditionalArgs: []string{"--order_output=no", "--proto:locations", "--noproto:default_values"},
	}

	logger.Info("starting benchmark",
		zap.String("workspace", opts.workspace),
		zap.String("query", opts.query),
		zap.Int("runs", opts.runs))

	var total time.Duration
	for i := range opts.runs {
		compute, err := benchmarkRun(logger, client, req, opts, i+1)
		if err != nil {
			return fmt.Errorf("run %d: %w", i+1, err)
		}
		total += compute
	}

	if opts.runs > 1 {
		logger.Info("average compute duration",
			zap.Duration("duration", (total/time.Duration(opts.runs)).Round(time.Millisecond)),
			zap.Int("runs", opts.runs))
	}
	return nil
}

func parseOptions() options {
	var opts options
	var excludeExternal bool
	flag.StringVar(&opts.bazelCmd, "bazel", "", "bazel binary to invoke (default: auto-detect bazel/bazelisk on PATH)")
	flag.StringVar(&opts.workspace, "workspace", ".", "workspace root to run bazel query in")
	flag.StringVar(&opts.query, "query", "", "bazel query expression (default: the standard nativeGraphRunner query)")
	flag.BoolVar(&excludeExternal, "exclude-external", false, "use deps(//...:all-targets) instead of including //external:all-targets")
	flag.BoolVar(&opts.bzlmod, "bzlmod", true, "mirror nativeGraphRunner for a Bzlmod repo: drop //external from the query, read repo marker hashes, hash with UseBzlmod")
	flag.StringVar(&opts.cpuProfile, "cpuprofile", "", "write a CPU profile of the targethasher phase to this file")
	flag.BoolVar(&opts.dump, "dump", false, "write the resulting graph chunks to stdout as JSON")
	flag.IntVar(&opts.runs, "runs", 1, "number of times to run the query (for benchmarking)")
	flag.DurationVar(&opts.timeout, "timeout", 30*time.Minute, "per-run timeout")
	flag.Parse()

	if opts.query == "" {
		opts.query = "//external:all-targets + deps(//...:all-targets)"
		if excludeExternal || opts.bzlmod {
			opts.query = "deps(//...:all-targets)"
		}
	}
	return opts
}

// benchmarkRun runs and times every phase of one nativeGraphRunner computation, and returns the combined
// duration of the phases that Compute performs.
func benchmarkRun(logger *zap.Logger, client bazel.Bazel, req *bazel.QueryRequest, opts options, run int) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	t := &phaseTimer{logger: logger, run: run}

	start := time.Now()
	resp, err := client.ExecuteQuery(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("query: %w", err)
	}
	t.record("bazel query", start, "targets", len(resp.Result.Target))

	hashConfig := targethasher.HashConfig{UseBzlmod: opts.bzlmod}
	if opts.bzlmod {
		if err := loadBzlmodInputs(ctx, t, opts, &hashConfig); err != nil {
			return 0, err
		}
	}

	start = time.Now()
	stopProfile, err := startCPUProfile(opts.cpuProfile)
	if err != nil {
		return 0, err
	}
	result, err := targethasher.FromProto(ctx, resp.Result, opts.workspace, hashConfig)
	stopProfile()
	if err != nil {
		return 0, fmt.Errorf("hash targets: %w", err)
	}
	t.record("target hasher", start, "targets", len(result.TargetNames))

	start = time.Now()
	chunks, err := tgmapper.ResultToGraphChunks(ctx, result, config.DefaultMaxMessageBytes)
	if err != nil {
		return 0, fmt.Errorf("convert to graph chunks: %w", err)
	}
	t.log("result to graph chunks", start, "chunks", len(chunks))

	if opts.dump {
		if err := dumpChunks(chunks); err != nil {
			return 0, err
		}
	}
	return t.total, nil
}

// loadBzlmodInputs fills in the inputs nativeGraphRunner reads for a Bzlmod repo before hashing.
func loadBzlmodInputs(ctx context.Context, t *phaseTimer, opts options, hashConfig *targethasher.HashConfig) error {
	start := time.Now()
	fileHashes, err := git.New(opts.workspace, t.logger).FileHashes(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("git file hashes: %w", err)
	}
	hashConfig.KnownSourceHashes = fileHashes
	t.record("git file hashes", start, "files", len(fileHashes))

	start = time.Now()
	markerHashes, err := targethasher.ReadRepoMarkerHashes(ctx, opts.workspace, opts.bazelCmd)
	if err != nil {
		return fmt.Errorf("read repo marker hashes: %w", err)
	}
	hashConfig.RepoMarkerHashes = markerHashes
	t.record("marker read", start, "repos", len(markerHashes))

	start = time.Now()
	repoMapping, err := bazel.RepoMapping(ctx, opts.workspace, opts.bazelCmd)
	if err != nil {
		return fmt.Errorf("read repo mapping: %w", err)
	}
	hashConfig.RepoMapping = repoMapping
	t.record("repo mapping", start, "repos", len(repoMapping))
	return nil
}

func dumpChunks(chunks []entity.GetTargetGraphResponse) error {
	m := jsonpb.Marshaler{Indent: "  "}
	for _, chunk := range chunks {
		if err := m.Marshal(os.Stdout, mapper.GetTargetGraphResponseToProto(&chunk)); err != nil {
			return fmt.Errorf("encode response: %w", err)
		}
		if _, err := io.WriteString(os.Stdout, "\n"); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}
	return nil
}

// phaseTimer logs the duration of each phase of a run and totals the phases that nativeGraphRunner.Compute performs.
type phaseTimer struct {
	logger *zap.Logger
	run    int
	total  time.Duration
}

// record logs a phase that Compute performs and adds its duration to the run total.
func (t *phaseTimer) record(phase string, start time.Time, countField string, count int) {
	t.total += t.log(phase, start, countField, count)
}

// log logs the time since start as the duration of phase, and returns it.
func (t *phaseTimer) log(phase string, start time.Time, countField string, count int) time.Duration {
	elapsed := time.Since(start)
	t.logger.Info(phase,
		zap.Int("run", t.run),
		zap.Duration("duration", elapsed.Round(time.Millisecond)),
		zap.Int(countField, count))
	return elapsed
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
		_ = f.Close()
		return nil, fmt.Errorf("starting cpu profile: %w", err)
	}
	return func() {
		pprof.StopCPUProfile()
		_ = f.Close()
	}, nil
}
