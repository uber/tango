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

package integration_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber-go/tally"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/zap/zaptest"
)

const (
	_itgApplicableMetric = "orchestrator.get_target_graph.itg_applicable"
	_itgDurationMetric   = "orchestrator.get_target_graph.itg_compute_duration"
)

// graphEdges fetches the target graph for build and returns every target name
// mapped to its sorted direct dependency names.
func graphEdges(t *testing.T, client pb.TangoYARPCClient, build *pb.BuildDescription) map[string][]string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	stream, err := client.GetTargetGraph(ctx, &pb.GetTargetGraphRequest{BuildDescription: build})
	require.NoError(t, err, "failed to initiate GetTargetGraph stream")

	raw := drainTargetGraphStream(t, stream)
	require.NotNil(t, raw.metadata, "expected metadata in response")
	names := raw.metadata.GetTargetIdMapping()

	edges := make(map[string][]string, len(raw.targets))
	for _, tgt := range raw.targets {
		deps := make([]string, 0, len(tgt.DirectDependencies))
		for _, id := range tgt.DirectDependencies {
			deps = append(deps, names[id])
		}
		sort.Strings(deps)
		edges[names[tgt.Id]] = deps
	}
	return edges
}

func counterValue(scope tally.TestScope, name string) int64 {
	var total int64
	for _, c := range scope.Snapshot().Counters() {
		if c.Name() == name && c.Tags()["repo"] == "integration-repository" {
			total += c.Value()
		}
	}
	return total
}

func durationSamples(scope tally.TestScope, name, result string) int {
	var n int
	for _, h := range scope.Snapshot().Histograms() {
		if h.Name() != name || h.Tags()["repo"] != "integration-repository" || h.Tags()["result"] != result {
			continue
		}
		for _, count := range h.Durations() {
			n += int(count)
		}
	}
	return n
}

// TestIntegration_ITG verifies that, with itg_enabled, the graph at the head
// revision is derived incrementally from the base revision's seeded graph and
// has the same targets and edges as a full query.
func TestIntegration_ITG(t *testing.T) {
	remote := repoRemote(t)
	base, head := fixtureBaseSHA(t), fixtureHeadSHA(t)

	scope := tally.NewTestScope("", nil)
	itgAddr, itgStore := startServerWithOptions(t, remote, zaptest.NewLogger(t), serverOptions{ITGEnabled: true, Scope: scope})
	itgClient := newClient(t, itgAddr)

	// The first request has a cold ITG cache, so it uses the full query and
	// seeds the cache for the base revision in the background.
	graphEdges(t, itgClient, buildDesc(remote, base))
	require.Eventually(t, func() bool {
		keys, err := itgStore.List(context.Background(), "itg/")
		return err == nil && len(keys) > 0
	}, time.Minute, 100*time.Millisecond, "expected the base graph to be seeded in the ITG cache")

	itgEdges := graphEdges(t, itgClient, buildDesc(remote, head))

	assert.Equal(t, int64(2), counterValue(scope, _itgApplicableMetric))
	assert.Equal(t, 1, durationSamples(scope, _itgDurationMetric, "fallback"), "cold base request should fall back")
	assert.Equal(t, 1, durationSamples(scope, _itgDurationMetric, "success"), "head request should be computed incrementally")

	fullAddr := startServer(t, remote)
	fullEdges := graphEdges(t, newClient(t, fullAddr), buildDesc(remote, head))
	assert.Equal(t, fullEdges, itgEdges, "incremental graph must match the full-query graph")
}
