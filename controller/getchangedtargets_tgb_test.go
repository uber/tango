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

package controller

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber-go/tally"
	"github.com/uber/tango/config"
	"github.com/uber/tango/core/cachekey"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
	orchestratormock "github.com/uber/tango/orchestrator/orchestratormock"
	pb "github.com/uber/tango/tangopb"
	tangomock "github.com/uber/tango/tangopb/tangopbmock"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap/zaptest"
)

// Full 40-char hex hashes: the TGB encoder is strict about hash shape.
var (
	tgbHash1    = strings.Repeat("aa", 20)
	tgbHash2Old = strings.Repeat("bb", 20)
	tgbHash2New = strings.Repeat("cc", 20)
	tgbHash3    = strings.Repeat("dd", 20)
	tgbHash4    = strings.Repeat("ee", 20)
)

// tgbTestGraphChunks builds the two-target graph the gob-era streamChunks
// test uses, parameterized on target2's hash.
func tgbTestGraphChunks(hash2 string) []entity.GetTargetGraphResponse {
	return []entity.GetTargetGraphResponse{
		{Targets: []entity.OptimizedTarget{
			{ID: 1, Hash: tgbHash1, RuleType: 100},
			{ID: 2, Hash: hash2, RuleType: 100, DirectDependencies: []int32{1}},
		}},
		{Metadata: &entity.Metadata{
			TargetIDMapping: map[int32]string{1: "//app:target1", 2: "//app:target2"},
			RuleTypeMapping: map[int32]string{100: "go_library"},
		}},
	}
}

// seedTreehash stores the sha→treehash mapping the request resolution reads.
func seedTreehash(t *testing.T, st storage.Storage, baseSha, treehash string) {
	t.Helper()
	key := cachekey.GetTreehashCachePath(entity.BuildDescription{Remote: "repo:go-code", BaseSha: baseSha})
	require.NoError(t, st.Put(t.Context(), storage.UploadRequest{Key: key, Reader: bytes.NewReader([]byte(treehash))}))
}

// changedTargetsSent collects the changed targets and merged metadata out of
// a captured response stream.
func changedTargetsSent(t *testing.T, sent []*pb.GetChangedTargetsResponse) ([]*pb.ChangedTarget, map[int32]string) {
	t.Helper()
	var changed []*pb.ChangedTarget
	idToName := map[int32]string{}
	for _, resp := range sent {
		if ct := resp.GetChangedTargets(); ct != nil {
			changed = append(changed, ct.GetChangedTargets()...)
		}
		if m := resp.GetMetadata(); m != nil {
			for id, name := range m.GetTargetIdMapping() {
				idToName[id] = name
			}
		}
	}
	return changed, idToName
}

// counterValue sums the values of all counters in the scope whose name
// contains the given substring.
func counterValue(scope tally.TestScope, substring string) int64 {
	var total int64
	for name, counter := range scope.Snapshot().Counters() {
		if strings.Contains(name, substring) {
			total += counter.Value()
		}
	}
	return total
}

// TestGetChangedTargets_TGBNativePath is the GraphFormat=tgb end-to-end: both
// revisions' graphs are stored as TGB blobs, the comparison runs on the
// readers' columnar form (never touching the orchestrator or the chunk
// pipeline), and with ShadowCompare on, the background targetdiff oracle
// reports a match.
func TestGetChangedTargets_TGBNativePath(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())
	var sent []*pb.GetChangedTargetsResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(resp *pb.GetChangedTargetsResponse, _ ...interface{}) error {
		sent = append(sent, resp)
		return nil
	}).AnyTimes()

	st := storage.NewMemoryStorage()
	seedTreehash(t, st, "sha1", "treehash1")
	seedTreehash(t, st, "sha2", "treehash2")
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash("repo:go-code", "treehash1", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunks(tgbHash2Old)))
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash("repo:go-code", "treehash2", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunks(tgbHash2New)))

	scope := tally.NewTestScope("", nil)
	c := NewController(context.Background(), Params{
		Logger:        zaptest.NewLogger(t),
		Storage:       st,
		Orchestrator:  orchestratormock.NewMockOrchestrator(ctrl), // no calls expected: both graphs are cached
		Scope:         scope,
		GraphFormat:   config.GraphFormatTGB,
		ShadowCompare: true,
	})

	request := changedTargetsRequest()
	request.OutputConfig = &pb.OutputConfig{MaxDistance: -1, IncludeHashes: true, IncludeTags: true, IncludeAttributes: true}
	require.NoError(t, c.GetChangedTargets(request, stream))

	changed, idToName := changedTargetsSent(t, sent)
	require.Len(t, changed, 1, "should detect exactly the hash-flipped target")
	assert.Equal(t, tgbHash2Old, changed[0].GetOldTarget().GetHash())
	assert.Equal(t, tgbHash2New, changed[0].GetNewTarget().GetHash())
	assert.Equal(t, int32(0), changed[0].GetDistance())
	assert.Equal(t, "//app:target2", idToName[changed[0].GetNewTarget().GetId()])

	assert.EqualValues(t, 1, counterValue(scope, "tgb_native_compare"), "comparison must take the TGB-native path")

	// The shadow oracle runs in a fire-and-forget goroutine; wait for its verdict.
	require.Eventually(t, func() bool {
		return counterValue(scope, "tgb_shadow_match") == 1
	}, 5*time.Second, 10*time.Millisecond, "shadow compare did not report a match")
	assert.EqualValues(t, 0, counterValue(scope, "tgb_shadow_mismatch"))
	assert.EqualValues(t, 0, counterValue(scope, "tgb_shadow_error"))
}

// tgbTestGraphChunksWithATFH builds a four-target graph with AllTargetsFileHashes
// set in the metadata, for testing the TGB AllTargetsFiles trigger path.
func tgbTestGraphChunksWithATFH(hash2 string, atfh map[string]string) []entity.GetTargetGraphResponse {
	return []entity.GetTargetGraphResponse{
		{Targets: []entity.OptimizedTarget{
			{ID: 1, Hash: tgbHash1, RuleType: 100},
			{ID: 2, Hash: hash2, RuleType: 100, DirectDependencies: []int32{1}},
			{ID: 3, Hash: tgbHash3, RuleType: 100},
			{ID: 4, Hash: tgbHash4, RuleType: 100, DirectDependencies: []int32{3}},
		}},
		{Metadata: &entity.Metadata{
			TargetIDMapping:      map[int32]string{1: "//app:target1", 2: "//app:target2", 3: "//lib:util", 4: "//lib:core"},
			RuleTypeMapping:      map[int32]string{100: "go_library"},
			AllTargetsFileHashes: atfh,
		}},
	}
}

// TestGetChangedTargets_TGBAllTargetsTrigger verifies that the TGB comparison
// path checks AllTargetsFileHashes and, when a configured file differs,
// reports every target in the second graph as changed with distance 0.
func TestGetChangedTargets_TGBAllTargetsTrigger(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())
	var sent []*pb.GetChangedTargetsResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(resp *pb.GetChangedTargetsResponse, _ ...interface{}) error {
		sent = append(sent, resp)
		return nil
	}).AnyTimes()

	st := storage.NewMemoryStorage()
	seedTreehash(t, st, "sha1", "treehash1")
	seedTreehash(t, st, "sha2", "treehash2")
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash("repo:go-code", "treehash1", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunksWithATFH(tgbHash2Old, map[string]string{".bazelrc": "old-hash"})))
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash("repo:go-code", "treehash2", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunksWithATFH(tgbHash2Old, map[string]string{".bazelrc": "new-hash"})))

	scope := tally.NewTestScope("", nil)
	c := NewController(context.Background(), Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      st,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
		Scope:        scope,
		GraphFormat:  config.GraphFormatTGB,
	})

	request := changedTargetsRequest()
	request.OutputConfig = &pb.OutputConfig{MaxDistance: -1, IncludeHashes: true}
	require.NoError(t, c.GetChangedTargets(request, stream))

	changed, _ := changedTargetsSent(t, sent)
	require.Len(t, changed, 4, "all targets from second graph should be reported as changed")
	for _, ct := range changed {
		assert.Equal(t, pb.CHANGE_TYPE_CHANGED, ct.GetChangeType())
		assert.Equal(t, int32(0), ct.GetDistance())
		assert.NotNil(t, ct.GetNewTarget())
	}
	assert.EqualValues(t, 1, counterValue(scope, "all_targets_triggered"))
	assert.EqualValues(t, 0, counterValue(scope, "tgb_native_compare"), "trigger should skip the normal TGB diff")
}

// TestGetChangedTargets_TGBAllTargetsNoTrigger verifies that the TGB path
// proceeds with normal comparison when AllTargetsFileHashes match.
func TestGetChangedTargets_TGBAllTargetsNoTrigger(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())
	var sent []*pb.GetChangedTargetsResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(resp *pb.GetChangedTargetsResponse, _ ...interface{}) error {
		sent = append(sent, resp)
		return nil
	}).AnyTimes()

	st := storage.NewMemoryStorage()
	seedTreehash(t, st, "sha1", "treehash1")
	seedTreehash(t, st, "sha2", "treehash2")
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash("repo:go-code", "treehash1", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunksWithATFH(tgbHash2Old, map[string]string{".bazelrc": "same-hash"})))
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash("repo:go-code", "treehash2", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunksWithATFH(tgbHash2New, map[string]string{".bazelrc": "same-hash"})))

	scope := tally.NewTestScope("", nil)
	c := NewController(context.Background(), Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      st,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
		Scope:        scope,
		GraphFormat:  config.GraphFormatTGB,
	})

	request := changedTargetsRequest()
	request.OutputConfig = &pb.OutputConfig{MaxDistance: -1, IncludeHashes: true}
	require.NoError(t, c.GetChangedTargets(request, stream))

	changed, _ := changedTargetsSent(t, sent)
	require.Len(t, changed, 1, "only the hash-flipped target should be changed")
	assert.EqualValues(t, 0, counterValue(scope, "all_targets_triggered"))
	assert.EqualValues(t, 1, counterValue(scope, "tgb_native_compare"), "should use normal TGB diff")
}

// TestGetChangedTargets_TGBMixedFormatFallsBack covers the transitional
// window right after a format flip: one revision's graph exists only as a
// pre-flip gob stream, the other as a TGB blob. The comparison must fall back
// to the incumbent chunk pipeline (draining the TGB reader's decoded form)
// and still produce the same answer.
func TestGetChangedTargets_TGBMixedFormatFallsBack(t *testing.T) {
	ctrl := gomock.NewController(t)
	stream := tangomock.NewMockTangoServiceGetChangedTargetsYARPCServer(ctrl)
	stream.EXPECT().Context().Return(t.Context())
	var sent []*pb.GetChangedTargetsResponse
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(func(resp *pb.GetChangedTargetsResponse, _ ...interface{}) error {
		sent = append(sent, resp)
		return nil
	}).AnyTimes()

	st := storage.NewMemoryStorage()
	seedTreehash(t, st, "sha1", "treehash1")
	seedTreehash(t, st, "sha2", "treehash2")
	// First revision predates the flip: gob only, at the gob key.
	require.NoError(t, storage.WriteGraphStream(t.Context(), st,
		cachekey.GetGraphByTreeHash("repo:go-code", "treehash1", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunks(tgbHash2Old)))
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash("repo:go-code", "treehash2", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunks(tgbHash2New)))

	scope := tally.NewTestScope("", nil)
	c := NewController(context.Background(), Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      st,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
		Scope:        scope,
		GraphFormat:  config.GraphFormatTGB,
	})

	request := changedTargetsRequest()
	request.OutputConfig = &pb.OutputConfig{MaxDistance: -1, IncludeHashes: true, IncludeTags: true, IncludeAttributes: true}
	require.NoError(t, c.GetChangedTargets(request, stream))

	changed, idToName := changedTargetsSent(t, sent)
	require.Len(t, changed, 1)
	assert.Equal(t, tgbHash2Old, changed[0].GetOldTarget().GetHash())
	assert.Equal(t, tgbHash2New, changed[0].GetNewTarget().GetHash())
	assert.Equal(t, "//app:target2", idToName[changed[0].GetNewTarget().GetId()])

	assert.EqualValues(t, 0, counterValue(scope, "tgb_native_compare"), "mixed formats must use the incumbent pipeline")
}
