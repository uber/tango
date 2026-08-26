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

type bazelDependencyMode struct {
	useBzlmod bool
	query     string
	queryArgs [2]string
}

func resolveBazelDependencyMode(enabled *bool) bazelDependencyMode {
	if enabled == nil || *enabled {
		return bazelDependencyMode{
			useBzlmod: true,
			query:     "deps(//...:all-targets)",
			queryArgs: [2]string{"--enable_bzlmod=true", "--enable_workspace=false"},
		}
	}
	return bazelDependencyMode{
		query:     "//external:all-targets + deps(//...:all-targets)",
		queryArgs: [2]string{"--enable_bzlmod=false", "--enable_workspace=true"},
	}
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

	mode := resolveBazelDependencyMode(g.config.BzlmodEnabled)
	additionalArgs := make([]string, 0, 3+len(g.config.BazelExtraArgs)+len(mode.queryArgs))
	additionalArgs = append(additionalArgs, "--order_output=no", "--proto:locations", "--noproto:default_values")
	additionalArgs = append(additionalArgs, g.config.BazelExtraArgs...)
	// Keep Tango's selected dependency mode last so programmatically-created
	// configs cannot override it with BazelExtraArgs.
	additionalArgs = append(additionalArgs, mode.queryArgs[:]...)

	bazelStart := time.Now()
	queryResult, err := g.bazel.ExecuteQuery(ctx, &bazel.QueryRequest{
		Query:          mode.query,
		StartupOptions: g.config.BazelStartupOptions,
		// --order_output=no will make Bazel execute query faster
		// --proto:locations: we need to get external file location to make CTC more accurate
		// --noproto: parameters exclude fields from the output that are not used for hashing anyways, making
		// proto blob smaller and serialization/deserialization faster
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

	// Copy configured exclusions so appending request-specific entries cannot
	// mutate the shared configuration's backing array.
	excludedRegex := append([]string(nil), g.config.ExcludedFiles...)
	excludedRegex = append(excludedRegex, g.extraExcludedFiles...)
	hashConfig := targethasher.HashConfig{
		KnownSourceHashes: knownSourceHashes,
		FullHashRepos:     g.config.FullHashRepos,
		ExcludedRegex:     excludedRegex,
		UseBzlmod:         mode.useBzlmod,
		AllTargetsFiles:   g.config.AllTargetsFiles,
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
