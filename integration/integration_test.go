// Copyright (c) 2026 Uber Technologies, Inc.
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

// Package integration_test implements integration tests for tango.
// Its tests spin up the tango server, create a client that connects to it,
// and calls its APIs using the xytan0056/bazel-fixture GitHub repository
// as the target — a purpose-built Bazel Go project with a known, stable
// dependency graph.
package integration_test

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/config"
	"github.com/uber/tango/controller"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/repomanager"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/orchestrator"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/yarpc"
	"go.uber.org/yarpc/api/transport"
	yarpcgrpc "go.uber.org/yarpc/transport/grpc"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

const (
	requestTimeout     = 20 * time.Minute
	configTemplateFile = "testdata/tango-config.yaml.tmpl"

	// fixtureRemote is the default remote for the purpose-built fixture repo.
	// Override with TANGO_FIXTURE_REMOTE for local testing against a clone.
	fixtureRemote = "https://github.com/xytan0056/bazel-fixture.git"

	// Pinned SHAs from the fixture repo. These are stable — the repo is
	// purpose-built for tango integration tests and does not change.
	//
	//   main      (ed5aaef): scaffold — 24 first-party targets.
	//   PR #1 head (7960f3b): adds audit + timeutil, drops mathutil,
	//             expands strutil, propagates handler signature change.
	fixtureBaseSHA = "92bf74d03177c7dc996fcc6323ef6f34bc5445e6"
	fixtureHeadSHA = "f0e8587544e33df2348ca9e8f31d030e542dceb0"

	// fixturePRURL is the canonical change URI for PR #1 in the fixture repo.
	fixturePRURL = "github://github.com/xytan0056/bazel-fixture/pull/1/" + fixtureHeadSHA
)

// fixtureRepo returns the remote URL to use for fixture-based tests.
func fixtureRepo() string {
	if env := os.Getenv("TANGO_FIXTURE_REMOTE"); env != "" {
		return env
	}
	return fixtureRemote
}

// repoRemote returns the repo remote from the TANGO_REPO_REMOTE env var.
// Used only by benchmarks, which test against the tango repo itself.
func repoRemote(t testing.TB) string {
	t.Helper()
	remote := os.Getenv("TANGO_REPO_REMOTE")
	require.NotEmpty(t, remote, "TANGO_REPO_REMOTE must be set (pass --test_env=TANGO_REPO_REMOTE=... to bazel test)")
	return remote
}

func writeConfig(t testing.TB, dir, remote, clonePath string) string {
	t.Helper()

	tmpl, err := template.ParseFiles(configTemplateFile)
	require.NoError(t, err, "failed to parse config template")

	configPath := filepath.Join(dir, "tango-config.yaml")
	f, err := os.Create(configPath)
	require.NoError(t, err, "failed to create config file")
	defer f.Close()

	// When the remote is a local path, use its tools/bazel. When it is a
	// URL (the fixture repo case), leave BazelCommand empty so tango
	// auto-downloads Bazelisk.
	var bazelCmd string
	if !isURL(remote) {
		bazelCmd = filepath.Join(remote, "tools", "bazel")
	}

	err = tmpl.Execute(f, struct {
		Remote       string
		ClonePath    string
		BazelCommand string
	}{
		Remote:       remote,
		ClonePath:    clonePath,
		BazelCommand: bazelCmd,
	})
	require.NoError(t, err, "failed to render config template")

	return configPath
}

func isURL(s string) bool {
	return len(s) > 8 && (s[:8] == "https://" || s[:7] == "http://" || s[:6] == "git://")
}

func startServer(t testing.TB, remote string) string {
	t.Helper()
	return startServerWithLogger(t, remote, zaptest.NewLogger(t))
}

func startServerWithLogger(t testing.TB, remote string, zl *zap.Logger) string {
	t.Helper()

	configDir := t.TempDir()
	clonePath := t.TempDir()

	configPath := writeConfig(t, configDir, remote, clonePath)

	appCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	store := storage.NewMemoryStorage()

	rm, err := repomanager.NewRepoManager(appCtx, repomanager.Params{
		Git:                  git.New(clonePath, zl),
		Logger:               zl,
		RepoManagerClonePath: clonePath,
		PoolSize:             2,
	})
	require.NoError(t, err, "failed to create repo manager")

	cfg, err := config.Parse(configPath)
	require.NoError(t, err, "failed to parse config")

	orch, err := orchestrator.NewNativeOrchestrator(appCtx, orchestrator.Params{
		Storage:     store,
		RepoManager: rm,
		Logger:      zl,
		GitFactory:  func(dir string) git.Interface { return git.New(dir, zl) },
		Config:      cfg,
	})
	require.NoError(t, err, "failed to create orchestrator")

	ctrl := controller.NewController(appCtx, controller.Params{
		Logger:       zl,
		Storage:      store,
		Orchestrator: orch,
	})

	grpcTransport := yarpcgrpc.NewTransport()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "failed to listen on dynamic port")

	dispatcher := yarpc.NewDispatcher(yarpc.Config{
		Name:     "tango",
		Inbounds: []transport.Inbound{grpcTransport.NewInbound(listener)},
	})
	dispatcher.Register(pb.BuildTangoYARPCProcedures(ctrl))

	require.NoError(t, dispatcher.Start(), "failed to start dispatcher")
	t.Cleanup(func() { assert.NoError(t, dispatcher.Stop()) })

	return listener.Addr().String()
}

func newClient(t testing.TB, addr string) pb.TangoYARPCClient {
	t.Helper()

	grpcTransport := yarpcgrpc.NewTransport()
	out := grpcTransport.NewSingleOutbound(addr)

	dispatcher := yarpc.NewDispatcher(yarpc.Config{
		Name: "tango-test-client",
		Outbounds: yarpc.Outbounds{
			"tango": {Stream: out},
		},
	})

	require.NoError(t, dispatcher.Start(), "failed to start client dispatcher")
	t.Cleanup(func() { assert.NoError(t, dispatcher.Stop()) })

	return pb.NewTangoYARPCClient(dispatcher.ClientConfig("tango"))
}

// rawGraph holds the full streamed response before any subgraph extraction.
type rawGraph struct {
	targets  []*pb.OptimizedTarget
	metadata *pb.Metadata
}

func drainTargetGraphStream(t *testing.T, stream pb.TangoServiceGetTargetGraphYARPCClient) rawGraph {
	t.Helper()
	defer func() { _ = stream.CloseSend() }()

	var result rawGraph
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err, "unexpected error receiving target graph chunk")

		switch item := msg.GetItem().(type) {
		case *pb.GetTargetGraphResponse_Targets:
			if item.Targets != nil {
				result.targets = append(result.targets, item.Targets.GetTargets()...)
			}
		case *pb.GetTargetGraphResponse_Metadata:
			result.metadata = mergeMetadata(result.metadata, item.Metadata)
		}
	}
	return result
}

// subgraph is a helper for constructing a focused subgraph from a raw proto graph.
// It returns a mapping of target names to list of dependency target names.
func subgraph(t *testing.T, raw rawGraph, roots ...string) map[string][]string {
	t.Helper()

	require.NotNil(t, raw.metadata, "subgraph: metadata is nil")

	nameByID := make(map[int32]string)
	idByName := make(map[string]int32)
	for id, name := range raw.metadata.GetTargetIdMapping() {
		nameByID[id] = name
		idByName[name] = id
	}

	depsByID := make(map[int32][]int32, len(raw.targets))
	for _, t := range raw.targets {
		depsByID[t.Id] = t.DirectDependencies
	}

	visited := make(map[int32]bool)
	queue := make([]int32, 0, len(roots))
	for _, root := range roots {
		id, ok := idByName[root]
		require.True(t, ok, "subgraph: root target %q not found in graph", root)
		queue = append(queue, id)
	}

	result := make(map[string][]string)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true

		name, ok := nameByID[id]
		require.True(t, ok, "subgraph: target ID %d has no name in metadata", id)

		var deps []string
		for _, depID := range depsByID[id] {
			if depName, ok := nameByID[depID]; ok {
				deps = append(deps, depName)
			}
			if !visited[depID] {
				queue = append(queue, depID)
			}
		}
		sort.Strings(deps)
		result[name] = deps
	}

	return result
}

type parsedChangedTargets struct {
	ByType    map[pb.ChangeType][]string
	Distances map[string]int32
}

func getChangedTargets(t testing.TB, client pb.TangoYARPCClient, first, second *pb.BuildDescription) parsedChangedTargets {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	stream, err := client.GetChangedTargets(ctx, &pb.GetChangedTargetsRequest{
		FirstRevision:  first,
		SecondRevision: second,
	})
	require.NoError(t, err, "failed to initiate GetChangedTargets stream")

	var (
		changedTargets []*pb.ChangedTarget
		metadata       *pb.Metadata
	)
	func() {
		defer stream.CloseSend()
		for {
			msg, err := stream.Recv()
			if err == io.EOF {
				break
			}
			require.NoError(t, err, "unexpected error receiving changed targets chunk")

			switch item := msg.GetItem().(type) {
			case *pb.GetChangedTargetsResponse_ChangedTargets:
				if item.ChangedTargets != nil {
					changedTargets = append(changedTargets, item.ChangedTargets.GetChangedTargets()...)
				}
			case *pb.GetChangedTargetsResponse_Metadata:
				metadata = mergeMetadata(metadata, item.Metadata)
			}
		}
	}()

	require.NotNil(t, metadata, "expected metadata in response")
	require.NotEmpty(t, metadata.GetTargetIdMapping(), "expected non-empty target ID mapping")

	mapping := metadata.GetTargetIdMapping()
	parsed := parsedChangedTargets{
		ByType:    make(map[pb.ChangeType][]string),
		Distances: make(map[string]int32, len(changedTargets)),
	}
	for _, ct := range changedTargets {
		target := ct.NewTarget
		if target == nil {
			target = ct.OldTarget
		}
		require.NotNil(t, target, "ChangedTarget has neither NewTarget nor OldTarget")
		name, ok := mapping[target.Id]
		require.True(t, ok, "target ID %d has no name in metadata", target.Id)
		parsed.ByType[ct.ChangeType] = append(parsed.ByType[ct.ChangeType], name)
		parsed.Distances[name] = ct.GetDistance()
	}
	for _, names := range parsed.ByType {
		sort.Strings(names)
	}
	return parsed
}

func mergeMetadata(existing, incoming *pb.Metadata) *pb.Metadata {
	if incoming == nil {
		return existing
	}
	if existing == nil {
		return incoming
	}
	for k, v := range incoming.GetTargetIdMapping() {
		existing.TargetIdMapping[k] = v
	}
	for k, v := range incoming.GetRuleTypeMapping() {
		existing.RuleTypeMapping[k] = v
	}
	for k, v := range incoming.GetTagMapping() {
		existing.TagMapping[k] = v
	}
	for k, v := range incoming.GetAttributeNameMapping() {
		existing.AttributeNameMapping[k] = v
	}
	for k, v := range incoming.GetAttributeStringValueMapping() {
		existing.AttributeStringValueMapping[k] = v
	}
	return existing
}

func TestIntegration_GetTargetGraph(t *testing.T) {
	remote := fixtureRepo()
	addr := startServer(t, remote)
	client := newClient(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	stream, err := client.GetTargetGraph(ctx, &pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{
			Strategy: pb.COMPUTATION_STRATEGY_UNSET,
			Remote:   remote,
			BaseSha:  fixtureBaseSHA,
		},
	})
	require.NoError(t, err, "failed to initiate GetTargetGraph stream")

	raw := drainTargetGraphStream(t, stream)
	require.NotNil(t, raw.metadata, "expected metadata in response")
	require.NotEmpty(t, raw.metadata.GetTargetIdMapping())
	require.NotEmpty(t, raw.metadata.GetRuleTypeMapping())

	// Collect all target names for debugging and subgraph checks.
	targetNames := make([]string, 0, len(raw.metadata.GetTargetIdMapping()))
	for _, name := range raw.metadata.GetTargetIdMapping() {
		targetNames = append(targetNames, name)
	}
	sort.Strings(targetNames)
	t.Logf("target count: %d, target names:\n%v", len(raw.targets), targetNames)

	totalEdges := 0
	for _, tgt := range raw.targets {
		totalEdges += len(tgt.DirectDependencies)
	}
	t.Logf("edge count: %d", totalEdges)

	t.Run("service layer depends on pkg layer", func(t *testing.T) {
		g := subgraph(t, raw, "//service/handlers:handlers")
		assert.Contains(t, g["//service/handlers:handlers"], "//pkg/logger:logger")
		assert.Contains(t, g["//service/handlers:handlers"], "//service/store:store")
	})

	t.Run("cmd depends on service and config", func(t *testing.T) {
		g := subgraph(t, raw, "//cmd/server:server_lib")
		assert.Contains(t, g["//cmd/server:server_lib"], "//service/api:api")
		assert.Contains(t, g["//cmd/server:server_lib"], "//service/config:config")
		assert.Contains(t, g["//cmd/server:server_lib"], "//pkg/logger:logger")
	})

	t.Run("external dependencies are present", func(t *testing.T) {
		g := subgraph(t, raw, "//pkg/logger:logger")
		loggerDeps := g["//pkg/logger:logger"]
		hasExternal := false
		for _, dep := range loggerDeps {
			if len(dep) > 0 && dep[0] == '@' {
				hasExternal = true
				break
			}
		}
		assert.True(t, hasExternal, "expected //pkg/logger:logger to have at least one external (@) dependency, got: %v", loggerDeps)
	})

	t.Run("proto targets exist", func(t *testing.T) {
		found := false
		for _, name := range targetNames {
			if name == "//proto/common:common_proto" || name == "//proto/api:api_proto" || name == "//proto/store:store_proto" {
				found = true
				break
			}
		}
		assert.True(t, found, "expected at least one proto_library target in the graph")
	})

	t.Run("embedded config file is a dependency", func(t *testing.T) {
		g := subgraph(t, raw, "//service/config:config")
		configDeps := g["//service/config:config"]
		hasEmbedded := false
		for _, dep := range configDeps {
			if dep == "//service/config:config.yaml" || dep == "//service/config:defaults.json" {
				hasEmbedded = true
				break
			}
		}
		assert.True(t, hasEmbedded, "expected //service/config:config to embed config.yaml or defaults.json, got: %v", configDeps)
	})
}

func TestIntegration_GetChangedTargets(t *testing.T) {
	remote := fixtureRepo()
	addr := startServer(t, remote)
	client := newClient(t, addr)

	t.Run("sha_comparison", func(t *testing.T) {
		// Compare main (ed5aaef) vs the PR head commit (7960f3b) directly
		// by SHA. This exercises the basic diff path without change-request
		// URL resolution.
		ct := getChangedTargets(t, client,
			&pb.BuildDescription{
				Strategy: pb.COMPUTATION_STRATEGY_UNSET,
				Remote:   remote,
				BaseSha:  fixtureBaseSHA,
			},
			&pb.BuildDescription{
				Strategy: pb.COMPUTATION_STRATEGY_UNSET,
				Remote:   remote,
				BaseSha:  fixtureHeadSHA,
			},
		)

		t.Logf("NEW: %v", ct.ByType[pb.CHANGE_TYPE_NEW])
		t.Logf("DELETED: %v", ct.ByType[pb.CHANGE_TYPE_DELETED])
		t.Logf("CHANGED: %v", ct.ByType[pb.CHANGE_TYPE_CHANGED])
		t.Logf("Distances: %v", ct.Distances)

		// mathutil was removed.
		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_DELETED], "expected deleted targets (mathutil)")

		// timeutil, audit, audit_proto were added.
		newTargets := ct.ByType[pb.CHANGE_TYPE_NEW]
		assert.NotEmpty(t, newTargets, "expected new targets (timeutil, audit)")

		// Changed targets should include propagation through the dep graph.
		changedTargets := ct.ByType[pb.CHANGE_TYPE_CHANGED]
		assert.NotEmpty(t, changedTargets, "expected changed targets")

		// Verify some specific expected targets in each category.
		newNames := ct.ByType[pb.CHANGE_TYPE_NEW]
		deletedNames := ct.ByType[pb.CHANGE_TYPE_DELETED]

		assertContainsTarget(t, newNames, "//pkg/timeutil:timeutil", "NEW")
		assertContainsTarget(t, newNames, "//service/audit:audit", "NEW")
		assertContainsTarget(t, deletedNames, "//pkg/mathutil:mathutil", "DELETED")

		// Distance checks: changed source files should be distance 0,
		// their reverse deps should be distance 1+.
		assertContainsTarget(t, changedTargets, "//service/handlers:handlers", "CHANGED")
		assertContainsTarget(t, changedTargets, "//service/api:api", "CHANGED")
		assertContainsTarget(t, changedTargets, "//cmd/server:server_lib", "CHANGED")
	})

	t.Run("pr_change_request", func(t *testing.T) {
		// Test the change-request URL code path: the second revision is
		// the base SHA + PR #1 layered on top. This exercises the full
		// github:// URI parsing, PR ref fetch, diff, and patch-apply
		// pipeline.
		ct := getChangedTargets(t, client,
			&pb.BuildDescription{
				Strategy: pb.COMPUTATION_STRATEGY_UNSET,
				Remote:   remote,
				BaseSha:  fixtureBaseSHA,
			},
			&pb.BuildDescription{
				Strategy: pb.COMPUTATION_STRATEGY_UNSET,
				Remote:   remote,
				BaseSha:  fixtureBaseSHA,
				Requests: []*pb.Request{{Url: fixturePRURL}},
			},
		)

		t.Logf("NEW: %v", ct.ByType[pb.CHANGE_TYPE_NEW])
		t.Logf("DELETED: %v", ct.ByType[pb.CHANGE_TYPE_DELETED])
		t.Logf("CHANGED: %v", ct.ByType[pb.CHANGE_TYPE_CHANGED])
		t.Logf("Distances: %v", ct.Distances)

		// The PR adds audit+timeutil, removes mathutil, and changes
		// handlers/api/cmd — same content as sha_comparison but exercising
		// the URL-based apply path.
		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_NEW], "expected new targets via PR")
		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_DELETED], "expected deleted targets via PR")
		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_CHANGED], "expected changed targets via PR")

		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_NEW], "//pkg/timeutil:timeutil", "NEW")
		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_NEW], "//service/audit:audit", "NEW")
		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_DELETED], "//pkg/mathutil:mathutil", "DELETED")
		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_CHANGED], "//service/handlers:handlers", "CHANGED")
	})
}

func assertContainsTarget(t testing.TB, names []string, target, category string) {
	t.Helper()
	for _, n := range names {
		if n == target {
			return
		}
	}
	t.Errorf("expected %s targets to contain %q, got: %v", category, target, names)
}
