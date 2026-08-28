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
// and calls its APIs against a dedicated Bazel fixture repository with a
// known, stable dependency graph.
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
)

func requiredEnv(t testing.TB, key string) string {
	t.Helper()
	v := os.Getenv(key)
	require.NotEmpty(t, v, "%s must be set (pass --test_env=%s=... to bazel test)", key, key)
	return v
}

func repoRemote(t testing.TB) string {
	t.Helper()
	return requiredEnv(t, "TANGO_REPO_REMOTE")
}

func fixtureBaseSHA(t testing.TB) string {
	t.Helper()
	return requiredEnv(t, "TANGO_BASE_SHA")
}

func fixtureHeadSHA(t testing.TB) string {
	t.Helper()
	return requiredEnv(t, "TANGO_HEAD_SHA")
}

func fixturePRURL(t testing.TB) string {
	t.Helper()
	return requiredEnv(t, "TANGO_PR_URL")
}

func graphFormat(t testing.TB) string {
	t.Helper()
	return requiredEnv(t, "TANGO_GRAPH_FORMAT")
}

func writeConfig(t testing.TB, dir, remote, clonePath string) string {
	t.Helper()

	tmpl, err := template.ParseFiles(configTemplateFile)
	require.NoError(t, err, "failed to parse config template")

	configPath := filepath.Join(dir, "tango-config.yaml")
	f, err := os.Create(configPath)
	require.NoError(t, err, "failed to create config file")
	defer f.Close()

	err = tmpl.Execute(f, struct {
		Remote       string
		ClonePath    string
		BazelCommand string
		GraphFormat  string
	}{
		Remote:      remote,
		ClonePath:   clonePath,
		GraphFormat: graphFormat(t),
	})
	require.NoError(t, err, "failed to render config template")

	return configPath
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

	cfg, err := config.Parse(configPath)
	require.NoError(t, err, "failed to parse config")

	rm, err := repomanager.NewRepoManager(appCtx, repomanager.Params{
		Git:                  git.New(clonePath, zl),
		Logger:               zl,
		RepoManagerClonePath: clonePath,
		PoolSize:             2,
		RepoConfig:           cfg,
	})
	require.NoError(t, err, "failed to create repo manager")

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
		RepoConfig:   cfg,
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

func buildDesc(remote, sha string) *pb.BuildDescription {
	return &pb.BuildDescription{
		Strategy: pb.COMPUTATION_STRATEGY_UNSET,
		Remote:   remote,
		BaseSha:  sha,
	}
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

func TestIntegration_GetTargetGraph(t *testing.T) {
	remote := repoRemote(t)
	addr := startServer(t, remote)
	client := newClient(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	stream, err := client.GetTargetGraph(ctx, &pb.GetTargetGraphRequest{
		BuildDescription: buildDesc(remote, fixtureBaseSHA(t)),
	})
	require.NoError(t, err, "failed to initiate GetTargetGraph stream")

	raw := drainTargetGraphStream(t, stream)
	require.NotNil(t, raw.metadata, "expected metadata in response")
	require.NotEmpty(t, raw.metadata.GetTargetIdMapping())
	require.NotEmpty(t, raw.metadata.GetRuleTypeMapping())

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

	t.Run("service_depends_on_pkg", func(t *testing.T) {
		g := subgraph(t, raw, "//service/handlers:handlers")
		assert.Contains(t, g["//service/handlers:handlers"], "//pkg/logger:logger")
		assert.Contains(t, g["//service/handlers:handlers"], "//service/store:store")
	})

	t.Run("cmd_depends_on_service_and_config", func(t *testing.T) {
		g := subgraph(t, raw, "//cmd/server:server_lib")
		assert.Contains(t, g["//cmd/server:server_lib"], "//service/api:api")
		assert.Contains(t, g["//cmd/server:server_lib"], "//service/config:config")
		assert.Contains(t, g["//cmd/server:server_lib"], "//pkg/logger:logger")
	})

	t.Run("external_dependencies", func(t *testing.T) {
		g := subgraph(t, raw, "//pkg/logger:logger")
		loggerDeps := g["//pkg/logger:logger"]
		hasExternal := false
		for _, dep := range loggerDeps {
			if len(dep) > 0 && dep[0] == '@' {
				hasExternal = true
				break
			}
		}
		assert.True(t, hasExternal, "expected at least one external (@) dependency, got: %v", loggerDeps)
	})

	t.Run("proto_targets", func(t *testing.T) {
		found := false
		for _, name := range targetNames {
			if name == "//proto/common:common_proto" || name == "//proto/api:api_proto" || name == "//proto/store:store_proto" {
				found = true
				break
			}
		}
		assert.True(t, found, "expected at least one proto_library target in the graph")
	})

	t.Run("embedded_config", func(t *testing.T) {
		g := subgraph(t, raw, "//service/config:config")
		configDeps := g["//service/config:config"]
		hasEmbedded := false
		for _, dep := range configDeps {
			if dep == "//service/config:config.yaml" || dep == "//service/config:defaults.json" {
				hasEmbedded = true
				break
			}
		}
		assert.True(t, hasEmbedded, "expected embedded config asset, got: %v", configDeps)
	})
}

func TestIntegration_GetChangedTargets(t *testing.T) {
	remote := repoRemote(t)
	addr := startServer(t, remote)
	client := newClient(t, addr)

	t.Run("sha_comparison", func(t *testing.T) {
		ct := getChangedTargets(t, client, buildDesc(remote, fixtureBaseSHA(t)), buildDesc(remote, fixtureHeadSHA(t)))

		t.Logf("NEW: %v", ct.ByType[pb.CHANGE_TYPE_NEW])
		t.Logf("DELETED: %v", ct.ByType[pb.CHANGE_TYPE_DELETED])
		t.Logf("CHANGED: %v", ct.ByType[pb.CHANGE_TYPE_CHANGED])
		t.Logf("Distances: %v", ct.Distances)

		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_DELETED])
		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_NEW])
		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_CHANGED])

		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_NEW], "//pkg/timeutil:timeutil", "NEW")
		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_NEW], "//proto/audit:audit_proto", "NEW")
		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_DELETED], "//pkg/mathutil:mathutil", "DELETED")
		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_CHANGED], "//service/handlers:handlers", "CHANGED")
		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_CHANGED], "//service/api:api", "CHANGED")
		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_CHANGED], "//cmd/server:server_lib", "CHANGED")

		assert.Equal(t, int32(0), ct.Distances["//pkg/strutil:strutil"])
		assert.Equal(t, int32(0), ct.Distances["//service/handlers:handlers"])
		assert.Equal(t, int32(1), ct.Distances["//service/api:api"])
		assert.Equal(t, int32(1), ct.Distances["//cmd/server:server_lib"])
		assert.Equal(t, int32(2), ct.Distances["//cmd/server:server"])
	})

	t.Run("pr_change_request", func(t *testing.T) {
		ct := getChangedTargets(t, client,
			buildDesc(remote, fixtureBaseSHA(t)),
			&pb.BuildDescription{
				Strategy: pb.COMPUTATION_STRATEGY_UNSET,
				Remote:   remote,
				BaseSha:  fixtureBaseSHA(t),
				Requests: []*pb.Request{{Url: fixturePRURL(t)}},
			},
		)

		t.Logf("NEW: %v", ct.ByType[pb.CHANGE_TYPE_NEW])
		t.Logf("DELETED: %v", ct.ByType[pb.CHANGE_TYPE_DELETED])
		t.Logf("CHANGED: %v", ct.ByType[pb.CHANGE_TYPE_CHANGED])
		t.Logf("Distances: %v", ct.Distances)

		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_NEW])
		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_DELETED])
		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_CHANGED])

		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_NEW], "//pkg/timeutil:timeutil", "NEW")
		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_NEW], "//proto/audit:audit_proto", "NEW")
		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_DELETED], "//pkg/mathutil:mathutil", "DELETED")
		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_CHANGED], "//service/handlers:handlers", "CHANGED")
	})
}
