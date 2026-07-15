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

package mapper

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/uber/tango/tangopb"
)

func TestBySize(t *testing.T) {
	t.Parallel()

	targets := make([]*pb.OptimizedTarget, 25)
	for i := range targets {
		targets[i] = &pb.OptimizedTarget{Id: int32(i + 1)}
	}
	maxBytes := targets[0].Size() * 10

	chunks, err := BySize(targets, maxBytes)
	require.NoError(t, err)
	require.Len(t, chunks, 3)
	assert.Len(t, chunks[0], 10)
	assert.Len(t, chunks[1], 10)
	assert.Len(t, chunks[2], 5)

	var total int
	for _, c := range chunks {
		for _, target := range c {
			assert.Equal(t, int32(total+1), target.Id)
			total++
		}
	}
	assert.Equal(t, 25, total)
}

func TestBySize_SingleOversizedItemShipsAlone(t *testing.T) {
	t.Parallel()

	oversized := &pb.OptimizedTarget{Id: 1, Hash: strings.Repeat("a", 1000)}
	small := &pb.OptimizedTarget{Id: 2}
	maxBytes := small.Size()

	chunks, err := BySize([]*pb.OptimizedTarget{oversized, small}, maxBytes)
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	assert.Equal(t, []*pb.OptimizedTarget{oversized}, chunks[0])
	assert.Equal(t, []*pb.OptimizedTarget{small}, chunks[1])
}

func TestBySize_EmptyInputReturnsOneEmptyChunk(t *testing.T) {
	t.Parallel()

	chunks, err := BySize([]*pb.OptimizedTarget{}, 100)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Empty(t, chunks[0])
}

func TestMapEntryWireSize_MatchesGeneratedSize(t *testing.T) {
	t.Parallel()

	m := &pb.Metadata{TargetIdMapping: map[int32]string{7: "hello world"}}
	assert.Equal(t, m.Size(), mapEntryWireSize(7, "hello world"))

	m2 := &pb.Metadata{TargetIdMapping: map[int32]string{1234567: strings.Repeat("x", 300)}}
	assert.Equal(t, m2.Size(), mapEntryWireSize(1234567, strings.Repeat("x", 300)))
}

func TestChunkMetadata_SplitsTargetMapByBytes(t *testing.T) {
	t.Parallel()

	targetMap := map[int32]string{1: "a", 2: "b", 3: "c", 4: "d"}
	ruleType := map[int32]string{1: "go_library"}
	entryBytes := mapEntryWireSize(1, "a")

	chunks, err := ChunkMetadata(targetMap, ruleType, nil, nil, nil, entryBytes*2)
	require.NoError(t, err)
	require.Len(t, chunks, 2)

	merged := map[int32]string{}
	for i, meta := range chunks {
		for k, v := range meta.TargetIdMapping {
			merged[k] = v
		}
		if i == 0 {
			assert.Equal(t, ruleType, meta.RuleTypeMapping)
		} else {
			assert.Empty(t, meta.RuleTypeMapping)
		}
	}
	assert.Equal(t, targetMap, merged)
}

func TestChunkMetadata_AllEmptyMapsReturnsOneEmptyChunk(t *testing.T) {
	t.Parallel()

	chunks, err := ChunkMetadata(nil, nil, nil, nil, nil, 100)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Empty(t, chunks[0].TargetIdMapping)
	assert.Empty(t, chunks[0].RuleTypeMapping)
}

func TestChunkMetadata_EmptyBigMapsStillCarrySmallMaps(t *testing.T) {
	t.Parallel()

	ruleType := map[int32]string{1: "go_library"}
	chunks, err := ChunkMetadata(nil, ruleType, nil, nil, nil, 100)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, ruleType, chunks[0].RuleTypeMapping)
}
