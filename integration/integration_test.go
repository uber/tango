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
// and calls its APIs using the tango GitHub repository itself as the target.
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
	"go.uber.org/zap/zaptest"
)

const (
	requestTimeout     = 10 * time.Minute
	configTemplateFile = "testdata/tango-config.yaml.tmpl"
)

func repoRemote(t *testing.T) string {
	t.Helper()
	remote := os.Getenv("TANGO_REPO_REMOTE")
	require.NotEmpty(t, remote, "TANGO_REPO_REMOTE must be set (pass --test_env=TANGO_REPO_REMOTE=... to bazel test)")
	return remote
}

func writeConfig(t *testing.T, dir, remote, clonePath, workerPath string) string {
	t.Helper()

	tmpl, err := template.ParseFiles(configTemplateFile)
	require.NoError(t, err, "failed to parse config template")

	configPath := filepath.Join(dir, "tango-config.yaml")
	f, err := os.Create(configPath)
	require.NoError(t, err, "failed to create config file")
	defer f.Close()

	err = tmpl.Execute(f, struct {
		Remote     string
		ClonePath  string
		WorkerPath string
	}{
		Remote:     remote,
		ClonePath:  clonePath,
		WorkerPath: workerPath,
	})
	require.NoError(t, err, "failed to render config template")

	return configPath
}

func startServer(t *testing.T, remote string) string {
	t.Helper()

	cacheDir := filepath.Join(t.TempDir(), "tango-e2e-cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	configDir := t.TempDir()
	clonePath := t.TempDir()
	workerPath := t.TempDir()

	configPath := writeConfig(t, configDir, remote, clonePath, workerPath)

	zl := zaptest.NewLogger(t)
	logger := zl.Sugar()

	appCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	store := storage.NewMemoryStorage()

	rm, err := repomanager.NewRepoManager(appCtx, repomanager.Params{
		Git:                  git.New(clonePath, logger),
		Logger:               logger,
		RepoManagerClonePath: clonePath,
		WorkerRootPath:       workerPath,
		PoolSize:             2,
	})
	require.NoError(t, err, "failed to create repo manager")

	cfg, err := config.Parse(configPath)
	require.NoError(t, err, "failed to parse config")

	orch, err := orchestrator.NewNativeOrchestrator(appCtx, orchestrator.Params{
		Storage:     store,
		RepoManager: rm,
		Logger:      logger,
		GitFactory:  func(dir string) git.Interface { return git.New(dir, logger) },
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

func newClient(t *testing.T, addr string) pb.TangoYARPCClient {
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

func getChangedTargets(t *testing.T, client pb.TangoYARPCClient, remote, firstSHA, secondSHA string) parsedChangedTargets {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	stream, err := client.GetChangedTargets(ctx, &pb.GetChangedTargetsRequest{
		FirstRevision: &pb.BuildDescription{
			Remote:  remote,
			BaseSha: firstSHA,
		},
		SecondRevision: &pb.BuildDescription{
			Remote:  remote,
			BaseSha: secondSHA,
		},
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
	remote := repoRemote(t)

	// Pinned SHA for deterministic assertions. The target count and edges are
	// fixed for a given treehash — they only change if this SHA is updated.
	const pinnedSHA = "74d1cd55155e5f4f43aa92b4e0146a0c528a0d96"

	addr := startServer(t, remote)
	client := newClient(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	stream, err := client.GetTargetGraph(ctx, &pb.GetTargetGraphRequest{
		BuildDescription: &pb.BuildDescription{
			Remote:  remote,
			BaseSha: pinnedSHA,
		},
	})
	require.NoError(t, err, "failed to initiate GetTargetGraph stream")

	raw := drainTargetGraphStream(t, stream)
	require.NotNil(t, raw.metadata, "expected metadata in response")
	require.NotEmpty(t, raw.metadata.GetTargetIdMapping())
	require.NotEmpty(t, raw.metadata.GetRuleTypeMapping())

	assert.Equal(t, 3673, len(raw.targets), "expected exact target count for pinned SHA")

	totalEdges := 0
	for _, tgt := range raw.targets {
		totalEdges += len(tgt.DirectDependencies)
	}
	assert.Equal(t, 8105, totalEdges, "expected exact edge count for pinned SHA")

	t.Run("controller sub-graph contains some correct well-known edges", func(t *testing.T) {
		controllerGraph := subgraph(t, raw, "//controller:controller")
		assert.Contains(t, controllerGraph["//controller:controller"], "//orchestrator:orchestrator")
		assert.Contains(t, controllerGraph["//controller:controller"], "//core/storage:storage")
		assert.Contains(t, controllerGraph["//controller:controller"], "//tangopb:tangopb")
		assert.Contains(t, controllerGraph["//orchestrator:orchestrator"], "//core/storage:storage")
		assert.Contains(t, controllerGraph["//orchestrator:orchestrator"], "//core/repomanager:repomanager")
		assert.Contains(t, controllerGraph["//orchestrator:orchestrator"], "//graphrunner:graphrunner")
	})

	t.Run("nodes correctly include external dependencies", func(t *testing.T) {
		configGraph := subgraph(t, raw, "//config:config")
		assert.Equal(t, []string{
			"//config:config.go",
			"//config:repository_config.go",
			"//config:service_config.go",
			"//config:storage_config.go",
			"@bazel_tools//tools/allowlists/function_transition_allowlist:function_transition_allowlist",
			"@com_github_goccy_go_yaml//:go-yaml",
			"@rules_go//:go_context_data",
		}, configGraph["//config:config"])
	})
}

func TestIntegration_GetChangedTargets(t *testing.T) {
	remote := repoRemote(t)

	addr := startServer(t, remote)
	client := newClient(t, addr)

	t.Run("changed_only", func(t *testing.T) {
		// Compare two adjacent commits:
		//   5716262 [core/storage] generic reader implementation (#145)
		//   74d1cd5 [core/workspace] fix silent drop of invalid scheme (#146)
		// The second commit changes core/workspace/request.go and its test.
		const firstSHA = "57162624a45965a7e783072c56561f91c5d4084d"
		const secondSHA = "74d1cd55155e5f4f43aa92b4e0146a0c528a0d96"

		ct := getChangedTargets(t, client, remote, firstSHA, secondSHA)

		assert.Empty(t, ct.ByType[pb.CHANGE_TYPE_NEW], "expected no new targets")
		assert.Empty(t, ct.ByType[pb.CHANGE_TYPE_DELETED], "expected no deleted targets")
		assert.ElementsMatch(t, []string{
			"//controller:controller",
			"//controller:controller_test",
			"//core/repomanager/mock:mock",
			"//core/repomanager:repomanager",
			"//core/repomanager:repomanager_test",
			"//core/workspace/workspacemock:workspacemock",
			"//core/workspace:request.go",
			"//core/workspace:request_test.go",
			"//core/workspace:workspace",
			"//core/workspace:workspace_test",
			"//example:example",
			"//example:example_lib",
			"//graphrunner/mock:mock",
			"//graphrunner:graphrunner",
			"//graphrunner:graphrunner_test",
			"//orchestrator/orchestratormock:orchestratormock",
			"//orchestrator:orchestrator",
			"//orchestrator:orchestrator_test",
		}, ct.ByType[pb.CHANGE_TYPE_CHANGED])

		assert.Equal(t, int32(0), ct.Distances["//core/workspace:request.go"])
		assert.Equal(t, int32(0), ct.Distances["//core/workspace:workspace"])
		assert.Equal(t, int32(1), ct.Distances["//orchestrator:orchestrator"])
		assert.Equal(t, int32(2), ct.Distances["//controller:controller"])
		assert.Equal(t, int32(3), ct.Distances["//example:example"])
	})

	t.Run("new_targets", func(t *testing.T) {
		// Compare two adjacent commits:
		//   046de2c (parent)
		//   1f2e3e9 Honor OutputConfig include_hashes/include_tags/include_attributes (#116)
		// The second commit adds controller/output_filter.go and output_filter_test.go.
		const firstSHA = "046de2c20b5492cd5606d32fd632a38b8b70c8f6"
		const secondSHA = "1f2e3e9245b159006cf2103becd51c5c1b6ec868"

		ct := getChangedTargets(t, client, remote, firstSHA, secondSHA)

		assert.Empty(t, ct.ByType[pb.CHANGE_TYPE_DELETED], "expected no deleted targets")
		assert.ElementsMatch(t, []string{
			"//controller:output_filter.go",
			"//controller:output_filter_test.go",
		}, ct.ByType[pb.CHANGE_TYPE_NEW])
		assert.ElementsMatch(t, []string{
			"//controller:BUILD.bazel",
			"//controller:controller",
			"//controller:controller_test",
			"//controller:getchangedtargets.go",
			"//controller:getchangedtargets_test.go",
			"//controller:gettargetgraph.go",
			"//example/client:client",
			"//example/client:client.go",
			"//example/client:client_lib",
			"//example:example",
			"//example:example_lib",
		}, ct.ByType[pb.CHANGE_TYPE_CHANGED])

		assert.Equal(t, int32(0), ct.Distances["//controller:output_filter.go"])
		assert.Equal(t, int32(0), ct.Distances["//controller:getchangedtargets.go"])
		assert.Equal(t, int32(0), ct.Distances["//controller:controller"])
		assert.Equal(t, int32(1), ct.Distances["//example:example_lib"])
		assert.Equal(t, int32(2), ct.Distances["//example:example"])
	})
}
