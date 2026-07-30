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

package graphrunner

import (
	"context"
	"time"

	"github.com/uber-go/tally"
	"github.com/uber/tango/config"
	"github.com/uber/tango/core/bazel"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/targethasher"
	"github.com/uber/tango/core/workspace"
	"github.com/uber/tango/observability/metrics"
)

type nativeGraphRunner struct {
	bazel              bazel.Bazel
	git                git.Interface
	config             config.RepositoryConfig
	extraExcludedFiles []string
	emitter            *metrics.Emitter
}

type NativeGraphRunnerParams struct {
	BazelClient        bazel.Bazel
	GitClient          git.Interface
	Config             config.RepositoryConfig
	ExtraExcludedFiles []string
	Scope              tally.Scope
}

// graph runner takes in a bazel query request and computes the graph
func NewNativeGraphRunner(p NativeGraphRunnerParams) GraphRunner {
	return &nativeGraphRunner{
		bazel:              p.BazelClient,
		git:                p.GitClient,
		config:             p.Config,
		extraExcludedFiles: p.ExtraExcludedFiles,
		emitter:            metrics.New(p.Scope).SubScope("graph_runner"),
	}
}

func (g *nativeGraphRunner) Compute(ctx context.Context, ws workspace.Workspace) (_ targethasher.Result, retErr error) {
	op := metrics.Begin(g.emitter, _opCompute, metrics.SlowDurationBuckets)
	defer func() { op.Complete(retErr) }()

	query := "//external:all-targets + deps(//...:all-targets)"
	if g.config.ExcludeExternalTargets {
		query = "deps(//...:all-targets)"
	}
	additionalArgs := append(
		[]string{"--order_output=no", "--proto:locations", "--noproto:default_values"},
		g.config.BazelExtraArgs...,
	)

	bazelStart := time.Now()
	queryResult, err := g.bazel.ExecuteQuery(ctx, &bazel.QueryRequest{
		Query:          query,
		StartupOptions: g.config.BazelStartupOptions,
		// --order_output=no will make Bazel execute query faster
		// --proto:locations: we need to get external file location to make CTC more accurate
		// --noproto: parameters exclude fields from the output that are not used for hashing anyways, making
		// proto blob smaller and serialization/deserialization faster
		// TODO: pass in --enable_workspace or --enable_bzlmod based on the config

		AdditionalArgs: additionalArgs,
	})
	g.emitter.DurationHistogram(_opCompute, "bazel_query_duration", metrics.SlowDurationBuckets).RecordDuration(time.Since(bazelStart))
	if err != nil {
		return targethasher.EmptyResult(), err
	}

	gitStart := time.Now()
	knownSourceHashes, err := g.git.FileHashes(ctx, "HEAD")
	g.emitter.DurationHistogram(_opCompute, "git_file_hashes_duration", metrics.FastDurationBuckets).RecordDuration(time.Since(gitStart))
	if err != nil {
		return targethasher.EmptyResult(), err
	}

	hashConfig := targethasher.HashConfig{
		KnownSourceHashes: knownSourceHashes,
		FullHashRepos:     g.config.FullHashRepos,
		ExcludedRegex:     append(g.config.ExcludedFiles, g.extraExcludedFiles...),
		UseBzlmod:         g.config.BzlmodEnabled,
	}

	hashStart := time.Now()
	res, err := targethasher.FromProto(ctx, queryResult.Result, ws.Path(), hashConfig)
	g.emitter.DurationHistogram(_opCompute, "target_hash_duration", metrics.FastDurationBuckets).RecordDuration(time.Since(hashStart))
	if err != nil {
		return targethasher.EmptyResult(), err
	}

	g.emitter.ValueHistogram(_opCompute, "target_count", metrics.LargeCountBuckets).RecordValue(float64(len(res.Targets)))
	return res, nil
}
