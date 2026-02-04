package bazelrunner

import (
	"github.com/uber/tango/core/config"
	"context"

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
	}
	// TODO: add bzlmod support
	res, err := targethasher.FromProto(ctx, queryResult.Result, ws.Path(), hashConfig)
	if err != nil {
		return targethasher.EmptyResult(), err
	}
	return res, nil
}
