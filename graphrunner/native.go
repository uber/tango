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

	"github.com/uber/tango/config"
	"github.com/uber/tango/core/bazel"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/targethasher"
	"github.com/uber/tango/core/workspace"
)

type nativeGraphRunner struct {
	bazel  bazel.Bazel
	git    git.Interface
	config config.RepositoryConfig
}

type NativeGraphRunnerParams struct {
	BazelClient bazel.Bazel
	GitClient   git.Interface
	Config      config.RepositoryConfig
}

// graph runner takes in a bazel query request and computes the graph
func NewNativeGraphRunner(p NativeGraphRunnerParams) GraphRunner {
	return &nativeGraphRunner{
		bazel:  p.BazelClient,
		git:    p.GitClient,
		config: p.Config,
	}
}

func (g *nativeGraphRunner) Compute(ctx context.Context, ws workspace.Workspace) (targethasher.Result, error) {
	query := "//external:all-targets + deps(//...:all-targets)"
	if g.config.ExcludeExternalTargets {
		query = "deps(//...:all-targets)"
	}
	queryResult, err := g.bazel.ExecuteQuery(ctx, &bazel.QueryRequest{
		Query: query,
		// --order_output=no will make Bazel execute query faster
		// --proto:locations: we need to get external file location to make CTC more accurate
		// --noproto: parameters exclude fields from the output that are not used for hashing anyways, making
		// proto blob smaller and serialization/deserialization faster
		// TODO: pass in --enable_workspace or --enable_bzlmod based on the config

		AdditionalArgs: []string{"--order_output=no", "--proto:locations", "--noproto:default_values"},
	})
	if err != nil {
		return targethasher.EmptyResult(), err
	}
	knownSourceHashes, err := g.git.FileHashes(ctx, "HEAD")
	if err != nil {
		return targethasher.EmptyResult(), err
	}
	hashConfig := targethasher.HashConfig{
		KnownSourceHashes: knownSourceHashes,
		FullHashRepos:     g.config.FullHashRepos,
		ExcludedFiles:     g.config.ExcludedFiles,
		UseBzlmod:         g.config.BzlmodEnabled,
	}

	res, err := targethasher.FromProto(ctx, queryResult.Result, ws.Path(), hashConfig)
	if err != nil {
		return targethasher.EmptyResult(), err
	}
	return res, nil
}
