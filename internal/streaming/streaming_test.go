package streaming

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/entity"
	pb "github.com/uber/tango/tangopb"
)

func TestSplitBySize(t *testing.T) {
	t.Parallel()

	targets := make([]*pb.OptimizedTarget, 25)
	for i := range targets {
		targets[i] = &pb.OptimizedTarget{Id: int32(i + 1)}
	}
	maxBytes := targets[0].Size() * 10

	groups, err := SplitBySize(targets, maxBytes)
	require.NoError(t, err)
	require.Len(t, groups, 3)
	assert.Len(t, groups[0], 10)
	assert.Len(t, groups[1], 10)
	assert.Len(t, groups[2], 5)

	var total int
	for _, g := range groups {
		for _, target := range g {
			assert.Equal(t, int32(total+1), target.Id)
			total++
		}
	}
	assert.Equal(t, 25, total)
}

func TestSplitBySize_SingleOversizedItemShipsAlone(t *testing.T) {
	t.Parallel()

	oversized := &pb.OptimizedTarget{Id: 1, Hash: strings.Repeat("a", 1000)}
	small := &pb.OptimizedTarget{Id: 2}
	maxBytes := small.Size()

	groups, err := SplitBySize([]*pb.OptimizedTarget{oversized, small}, maxBytes)
	require.NoError(t, err)
	require.Len(t, groups, 2)
	assert.Equal(t, []*pb.OptimizedTarget{oversized}, groups[0])
	assert.Equal(t, []*pb.OptimizedTarget{small}, groups[1])
}

func TestSplitBySize_EmptyInputReturnsOneEmptyGroup(t *testing.T) {
	t.Parallel()

	groups, err := SplitBySize([]*pb.OptimizedTarget{}, 100)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Empty(t, groups[0])
}

func TestMapEntryWireSize_MatchesGeneratedSize(t *testing.T) {
	t.Parallel()

	m := &pb.Metadata{TargetIdMapping: map[int32]string{7: "hello world"}}
	assert.Equal(t, m.Size(), mapEntryWireSize(7, "hello world"))

	m2 := &pb.Metadata{TargetIdMapping: map[int32]string{1234567: strings.Repeat("x", 300)}}
	assert.Equal(t, m2.Size(), mapEntryWireSize(1234567, strings.Repeat("x", 300)))
}

func TestSplitMetadata_SplitsTargetMapByBytes(t *testing.T) {
	t.Parallel()

	targetMap := map[int32]string{1: "a", 2: "b", 3: "c", 4: "d"}
	ruleType := map[int32]string{1: "go_library"}
	entryBytes := mapEntryWireSize(1, "a")

	metas, err := SplitMetadata(targetMap, ruleType, nil, nil, nil, entryBytes*2)
	require.NoError(t, err)
	require.Len(t, metas, 2)

	merged := map[int32]string{}
	for i, meta := range metas {
		for k, v := range meta.TargetIDMapping {
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

func TestSplitMetadata_AllEmptyMapsReturnsOneEmptyMessage(t *testing.T) {
	t.Parallel()

	metas, err := SplitMetadata(nil, nil, nil, nil, nil, 100)
	require.NoError(t, err)
	require.Len(t, metas, 1)
	assert.Empty(t, metas[0].TargetIDMapping)
	assert.Empty(t, metas[0].RuleTypeMapping)
}

func TestSplitMetadata_EmptyBigMapsStillCarrySmallMaps(t *testing.T) {
	t.Parallel()

	ruleType := map[int32]string{1: "go_library"}
	metas, err := SplitMetadata(nil, ruleType, nil, nil, nil, 100)
	require.NoError(t, err)
	require.Len(t, metas, 1)
	assert.Equal(t, ruleType, metas[0].RuleTypeMapping)
}

func TestSplitTargetGraph(t *testing.T) {
	t.Parallel()

	numTargets := 50
	targets := make([]entity.OptimizedTarget, numTargets)
	for i := range targets {
		targets[i] = entity.OptimizedTarget{ID: int32(i + 1), Hash: "ab", RuleType: 1}
	}
	meta := &entity.Metadata{
		TargetIDMapping: map[int32]string{1: "//pkg:a"},
		RuleTypeMapping: map[int32]string{1: "go_library"},
	}

	protoSize := optimizedTargetToProto(&targets[0]).Size()

	tests := []struct {
		name             string
		maxMessageBytes  int
		wantTargetChunks int
	}{
		{name: "25 per chunk", maxMessageBytes: protoSize * 25, wantTargetChunks: 2},
		{name: "10 per chunk", maxMessageBytes: protoSize * 10, wantTargetChunks: 5},
		{name: "all in one", maxMessageBytes: protoSize * 100, wantTargetChunks: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			chunks, err := SplitTargetGraph(targets, meta, tt.maxMessageBytes)
			require.NoError(t, err)

			var targetChunks, totalTargets int
			for _, c := range chunks {
				if len(c.Targets) > 0 || c.Metadata == nil {
					targetChunks++
					totalTargets += len(c.Targets)
				}
			}
			assert.Equal(t, tt.wantTargetChunks, targetChunks)
			assert.Equal(t, numTargets, totalTargets)
		})
	}
}
