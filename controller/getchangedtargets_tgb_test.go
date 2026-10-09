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
	"context"
	"fmt"
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
	key := cachekey.GetTreehashCachePath(testRepositoryID("repo:go-code"), entity.BuildDescription{Remote: "repo:go-code", BaseSha: baseSha})
	require.NoError(t, st.Put(t.Context(), storage.UploadRequest{Key: key, Reader: strings.NewReader(treehash)}))
}

// changedTargetsFromResult collects the changed targets and the target
// ID->name mapping out of a controller-level GetChangedTargets result.
func changedTargetsFromResult(result entity.ChangedTargetsResult) ([]entity.ChangedTarget, map[int32]string) {
	idToName := map[int32]string{}
	if result.Metadata != nil {
		for id, name := range result.Metadata.TargetIDMapping {
			idToName[id] = name
		}
	}
	return result.ChangedTargets, idToName
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

	st := storage.NewMemoryStorage()
	seedTreehash(t, st, "sha1", "treehash1")
	seedTreehash(t, st, "sha2", "treehash2")
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash(testRepositoryID("repo:go-code"), "treehash1", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunks(tgbHash2Old)))
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash(testRepositoryID("repo:go-code"), "treehash2", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunks(tgbHash2New)))

	scope := tally.NewTestScope("", nil)
	c := NewController(context.Background(), Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      st,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl), // no calls expected: both graphs are cached
		Scope:        scope,
		GraphConfig:  staticGraphConfig(config.GraphConfig{Format: config.GraphFormatTGB, ShadowCompare: true}),
	})

	responses, err := c.GetChangedTargets(t.Context(), changedTargetsRequest(), testRepositoryConfig)
	require.NoError(t, err)

	changed, idToName := changedTargetsFromResult(responses)
	require.Len(t, changed, 1, "should detect exactly the hash-flipped target")
	assert.Equal(t, tgbHash2Old, changed[0].OldTarget.Hash)
	assert.Equal(t, tgbHash2New, changed[0].NewTarget.Hash)
	assert.Equal(t, int32(0), changed[0].Distance)
	assert.Equal(t, "//app:target2", idToName[changed[0].NewTarget.ID])

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

func tgbAllTargetsClassificationBefore(atfh map[string]string) []entity.GetTargetGraphResponse {
	return []entity.GetTargetGraphResponse{
		{Targets: []entity.OptimizedTarget{
			{ID: 1, Hash: tgbHash1, RuleType: 100},
			{ID: 2, Hash: tgbHash2Old, RuleType: 100, DirectDependencies: []int32{1}},
			{ID: 3, Hash: tgbHash3, RuleType: 100},
		}},
		{Metadata: &entity.Metadata{
			TargetIDMapping: map[int32]string{
				1: "//app:stable",
				2: "//app:changed",
				3: "//legacy:deleted",
			},
			RuleTypeMapping:      map[int32]string{100: "go_library"},
			AllTargetsFileHashes: atfh,
		}},
	}
}

func tgbAllTargetsClassificationAfter(atfh map[string]string) []entity.GetTargetGraphResponse {
	return []entity.GetTargetGraphResponse{
		{Targets: []entity.OptimizedTarget{
			{ID: 10, Hash: tgbHash1, RuleType: 200},
			{ID: 20, Hash: tgbHash2New, RuleType: 200, DirectDependencies: []int32{10}},
			{ID: 40, Hash: tgbHash4, RuleType: 200},
		}},
		{Metadata: &entity.Metadata{
			TargetIDMapping: map[int32]string{
				10: "//app:stable",
				20: "//app:changed",
				40: "//app:new",
			},
			RuleTypeMapping:      map[int32]string{200: "go_library"},
			AllTargetsFileHashes: atfh,
		}},
	}
}

// TestGetChangedTargets_TGBAllTargetsTrigger verifies that the TGB comparison
// path checks AllTargetsFileHashes and, when a configured file differs,
// reports every target in the second graph as changed with distance 0.
func TestGetChangedTargets_TGBAllTargetsTrigger(t *testing.T) {
	ctrl := gomock.NewController(t)

	st := storage.NewMemoryStorage()
	seedTreehash(t, st, "sha1", "treehash1")
	seedTreehash(t, st, "sha2", "treehash2")
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash(testRepositoryID("repo:go-code"), "treehash1", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunksWithATFH(tgbHash2Old, map[string]string{".bazelrc": "old-hash"})))
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash(testRepositoryID("repo:go-code"), "treehash2", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunksWithATFH(tgbHash2Old, map[string]string{".bazelrc": "new-hash"})))

	scope := tally.NewTestScope("", nil)
	c := NewController(context.Background(), Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      st,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
		Scope:        scope,
		GraphConfig:  staticGraphConfig(config.GraphConfig{Format: config.GraphFormatTGB}),
	})

	responses, err := c.GetChangedTargets(t.Context(), changedTargetsRequest(), testRepositoryConfig)
	require.NoError(t, err)

	changed, _ := changedTargetsFromResult(responses)
	require.Len(t, changed, 4, "all targets from second graph should be reported as changed")
	for _, ct := range changed {
		assert.Equal(t, entity.ChangeTypeChanged, ct.ChangeType)
		assert.Equal(t, int32(0), ct.Distance)
		assert.NotNil(t, ct.NewTarget)
	}
	assert.EqualValues(t, 1, counterValue(scope, "all_targets_triggered"))
	assert.EqualValues(t, 0, counterValue(scope, "tgb_native_compare"), "trigger should skip the normal TGB diff")
}

func TestGetChangedTargets_TGBAllTargetsTriggerPreservesMembershipChanges(t *testing.T) {
	ctrl := gomock.NewController(t)

	st := storage.NewMemoryStorage()
	seedTreehash(t, st, "sha1", "treehash1")
	seedTreehash(t, st, "sha2", "treehash2")
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash(testRepositoryID("repo:go-code"), "treehash1", entity.ComputationStrategyUnset, nil),
		tgbAllTargetsClassificationBefore(map[string]string{".bazelrc": "old-hash"})))
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash(testRepositoryID("repo:go-code"), "treehash2", entity.ComputationStrategyUnset, nil),
		tgbAllTargetsClassificationAfter(map[string]string{".bazelrc": "new-hash"})))

	scope := tally.NewTestScope("", nil)
	c := NewController(context.Background(), Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      st,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
		Scope:        scope,
		GraphConfig:  staticGraphConfig(config.GraphConfig{Format: config.GraphFormatTGB}),
	})

	responses, err := c.GetChangedTargets(t.Context(), changedTargetsRequest(), testRepositoryConfig)
	require.NoError(t, err)

	changed, idToName := changedTargetsFromResult(responses)
	require.Len(t, changed, 4)
	byName := make(map[string]entity.ChangedTarget, len(changed))
	for _, target := range changed {
		optimized := target.NewTarget
		if optimized == nil {
			optimized = target.OldTarget
		}
		require.NotNil(t, optimized)
		name := idToName[optimized.ID]
		require.NotEmpty(t, name)
		byName[name] = target
	}

	assertChange := func(name string, wantType entity.ChangeType, wantOld, wantNew bool) {
		t.Helper()
		target, ok := byName[name]
		require.True(t, ok, name)
		assert.Equal(t, wantType, target.ChangeType, name)
		assert.Equal(t, int32(0), target.Distance, name)
		if wantOld {
			require.NotNil(t, target.OldTarget, name)
			assert.Equal(t, name, idToName[target.OldTarget.ID])
		} else {
			assert.Nil(t, target.OldTarget, name)
		}
		if wantNew {
			require.NotNil(t, target.NewTarget, name)
			assert.Equal(t, name, idToName[target.NewTarget.ID])
		} else {
			assert.Nil(t, target.NewTarget, name)
		}
	}
	assertChange("//app:stable", entity.ChangeTypeChanged, true, true)
	assertChange("//app:changed", entity.ChangeTypeChanged, true, true)
	assertChange("//app:new", entity.ChangeTypeNew, false, true)
	assertChange("//legacy:deleted", entity.ChangeTypeDeleted, true, false)

	assert.EqualValues(t, 1, counterValue(scope, "all_targets_triggered"))
	assert.EqualValues(t, 0, counterValue(scope, "tgb_native_compare"), "trigger should skip the normal TGB diff")
}

// TestGetChangedTargets_TGBAllTargetsNoTrigger verifies that the TGB path
// proceeds with normal comparison when AllTargetsFileHashes match.
func TestGetChangedTargets_TGBAllTargetsNoTrigger(t *testing.T) {
	ctrl := gomock.NewController(t)

	st := storage.NewMemoryStorage()
	seedTreehash(t, st, "sha1", "treehash1")
	seedTreehash(t, st, "sha2", "treehash2")
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash(testRepositoryID("repo:go-code"), "treehash1", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunksWithATFH(tgbHash2Old, map[string]string{".bazelrc": "same-hash"})))
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash(testRepositoryID("repo:go-code"), "treehash2", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunksWithATFH(tgbHash2New, map[string]string{".bazelrc": "same-hash"})))

	scope := tally.NewTestScope("", nil)
	c := NewController(context.Background(), Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      st,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
		Scope:        scope,
		GraphConfig:  staticGraphConfig(config.GraphConfig{Format: config.GraphFormatTGB}),
	})

	responses, err := c.GetChangedTargets(t.Context(), changedTargetsRequest(), testRepositoryConfig)
	require.NoError(t, err)

	changed, _ := changedTargetsFromResult(responses)
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

	st := storage.NewMemoryStorage()
	seedTreehash(t, st, "sha1", "treehash1")
	seedTreehash(t, st, "sha2", "treehash2")
	// First revision predates the flip: gob only, at the gob key.
	require.NoError(t, storage.WriteGraphStream(t.Context(), st,
		cachekey.GetGraphByTreeHash(testRepositoryID("repo:go-code"), "treehash1", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunks(tgbHash2Old)))
	require.NoError(t, storage.WriteTGBGraph(t.Context(), st,
		cachekey.GetTGBGraphByTreeHash(testRepositoryID("repo:go-code"), "treehash2", entity.ComputationStrategyUnset, nil),
		tgbTestGraphChunks(tgbHash2New)))

	scope := tally.NewTestScope("", nil)
	c := NewController(context.Background(), Params{
		Logger:       zaptest.NewLogger(t),
		Storage:      st,
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
		Scope:        scope,
		GraphConfig:  staticGraphConfig(config.GraphConfig{Format: config.GraphFormatTGB}),
	})

	responses, err := c.GetChangedTargets(t.Context(), changedTargetsRequest(), testRepositoryConfig)
	require.NoError(t, err)

	changed, idToName := changedTargetsFromResult(responses)
	require.Len(t, changed, 1)
	assert.Equal(t, tgbHash2Old, changed[0].OldTarget.Hash)
	assert.Equal(t, tgbHash2New, changed[0].NewTarget.Hash)
	assert.Equal(t, "//app:target2", idToName[changed[0].NewTarget.ID])

	assert.EqualValues(t, 0, counterValue(scope, "tgb_native_compare"), "mixed formats must use the incumbent pipeline")
}

// TestGetChangedTargets_CacheWriteChunksResultBySize verifies that, although
// the controller returns an unchunked entity.ChangedTargetsResult to its
// caller, the compared-targets cache entry it writes in the background is
// still split into size-bounded chunks (the same storage format main wrote)
// rather than the two whole, unbounded pieces the result naturally splits
// into. This matters because the cache read path on an older binary sharing
// the same storage sends each stored chunk to the wire as one message
// without re-chunking, so an oversized stored chunk would produce an
// oversized wire message.
func TestGetChangedTargets_CacheWriteChunksResultBySize(t *testing.T) {
	ctrl := gomock.NewController(t)
	st := storage.NewMemoryStorage()

	seedTreehash(t, st, "sha1", "treehash1")
	seedTreehash(t, st, "sha2", "treehash2")

	repositoryID := testRepositoryID("repo:go-code")
	require.NoError(t, storage.WriteGraphStream(t.Context(), st,
		cachekey.GetGraphByTreeHash(repositoryID, "treehash1", entity.ComputationStrategyUnset, nil),
		[]entity.GetTargetGraphResponse{{Metadata: &entity.Metadata{}}}))

	const targetCount = 30
	targets := make([]entity.OptimizedTarget, targetCount)
	targetIDMapping := make(map[int32]string, targetCount)
	for i := 0; i < targetCount; i++ {
		targets[i] = entity.OptimizedTarget{ID: int32(i), Hash: fmt.Sprintf("h%d", i)}
		targetIDMapping[int32(i)] = fmt.Sprintf("//app:t%d", i)
	}
	require.NoError(t, storage.WriteGraphStream(t.Context(), st,
		cachekey.GetGraphByTreeHash(repositoryID, "treehash2", entity.ComputationStrategyUnset, nil),
		[]entity.GetTargetGraphResponse{{
			Targets:  targets,
			Metadata: &entity.Metadata{TargetIDMapping: targetIDMapping},
		}}))

	const maxMessageBytes = 40
	c := NewController(context.Background(), Params{
		Logger:          zaptest.NewLogger(t),
		Storage:         st,
		Orchestrator:    orchestratormock.NewMockOrchestrator(ctrl), // no calls expected: both graphs are cached
		MaxMessageBytes: maxMessageBytes,
	})

	result, err := c.GetChangedTargets(t.Context(), changedTargetsRequest(), testRepositoryConfig)
	require.NoError(t, err)
	require.Len(t, result.ChangedTargets, targetCount, "every target in the second revision is new")

	cacheKey := cachekey.GetComparedTargetsCachePath(repositoryID, "treehash1", "treehash2", nil)
	var chunks []entity.GetChangedTargetsResponse
	require.Eventually(t, func() bool {
		resp, getErr := st.Get(t.Context(), storage.DownloadRequest{Key: cacheKey})
		if getErr != nil {
			return false
		}
		defer func() { _ = resp.ReadCloser.Close() }()
		reader, readerErr := storage.NewChangedTargetsReader(t.Context(), st, cacheKey)
		if readerErr != nil {
			return false
		}
		defer func() { _ = reader.Close() }()
		chunks = nil
		for {
			chunk, readErr := reader.Read()
			if readErr != nil {
				break
			}
			chunks = append(chunks, chunk)
		}
		return len(chunks) > 0
	}, 2*time.Second, 10*time.Millisecond, "cache write never landed")

	require.Greater(t, len(chunks), 2, "a small maxMessageBytes must split the result into more than the two whole, unbounded pieces")
	for _, chunk := range chunks {
		if len(chunk.ChangedTargets) == 0 {
			continue
		}
		size := 0
		for _, ct := range chunk.ChangedTargets {
			size += ct.Size()
		}
		assert.LessOrEqual(t, size, maxMessageBytes, "each stored changed-targets chunk must stay within maxMessageBytes")
	}
}
