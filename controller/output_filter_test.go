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

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/uber/tango/tangopb"
)

func fullTarget() *pb.OptimizedTarget {
	return &pb.OptimizedTarget{
		Id:                 1,
		Hash:               "h1",
		DirectDependencies: []int32{2, 3},
		Tags:               []int32{10, 11},
		RuleType:           7,
		Root:               true,
		External:           false,
		Attributes:         map[int32]int32{100: 200, 101: 201},
	}
}

func TestApplyOptimizedTargetOutputConfig_NilConfigStripsAll(t *testing.T) {
	got := applyOptimizedTargetOutputConfig(fullTarget(), nil)
	require.NotNil(t, got)
	assert.Equal(t, "", got.GetHash(), "hash should be stripped when config is nil")
	assert.Nil(t, got.GetTags(), "tags should be stripped when config is nil")
	assert.Nil(t, got.GetAttributes(), "attributes should be stripped when config is nil")
	// Non-filtered fields preserved.
	assert.Equal(t, int32(1), got.GetId())
	assert.Equal(t, []int32{2, 3}, got.GetDirectDependencies())
	assert.Equal(t, int32(7), got.GetRuleType())
	assert.True(t, got.GetRoot())
}

func TestApplyOptimizedTargetOutputConfig_AllIncludesPassesThrough(t *testing.T) {
	cfg := &pb.OutputConfig{IncludeHashes: true, IncludeTags: true, IncludeAttributes: true}
	src := fullTarget()
	got := applyOptimizedTargetOutputConfig(src, cfg)
	// Should be the same pointer — no copy made.
	assert.Same(t, src, got, "no copy should be made when nothing needs stripping")
}

func TestApplyOptimizedTargetOutputConfig_PartialFlags(t *testing.T) {
	cases := []struct {
		name        string
		cfg         *pb.OutputConfig
		expectHash  string
		expectTags  []int32
		expectAttrs map[int32]int32
	}{
		{
			name:        "hashes only",
			cfg:         &pb.OutputConfig{IncludeHashes: true},
			expectHash:  "h1",
			expectTags:  nil,
			expectAttrs: nil,
		},
		{
			name:        "tags only",
			cfg:         &pb.OutputConfig{IncludeTags: true},
			expectHash:  "",
			expectTags:  []int32{10, 11},
			expectAttrs: nil,
		},
		{
			name:        "attributes only",
			cfg:         &pb.OutputConfig{IncludeAttributes: true},
			expectHash:  "",
			expectTags:  nil,
			expectAttrs: map[int32]int32{100: 200, 101: 201},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applyOptimizedTargetOutputConfig(fullTarget(), tc.cfg)
			require.NotNil(t, got)
			assert.Equal(t, tc.expectHash, got.GetHash())
			assert.Equal(t, tc.expectTags, got.GetTags())
			assert.Equal(t, tc.expectAttrs, got.GetAttributes())
		})
	}
}

func TestApplyOptimizedTargetOutputConfig_DoesNotMutateSource(t *testing.T) {
	src := fullTarget()
	_ = applyOptimizedTargetOutputConfig(src, nil)
	// Source unchanged — important because cache goroutine may concurrently write the same proto.
	assert.Equal(t, "h1", src.GetHash())
	assert.Equal(t, []int32{10, 11}, src.GetTags())
	assert.Equal(t, map[int32]int32{100: 200, 101: 201}, src.GetAttributes())
}

func TestApplyOptimizedTargetOutputConfig_NilTarget(t *testing.T) {
	assert.Nil(t, applyOptimizedTargetOutputConfig(nil, nil))
}

func TestApplyChangedTargetOutputConfig_StripsBothSides(t *testing.T) {
	src := &pb.ChangedTarget{
		ChangeType: pb.CHANGE_TYPE_CHANGED,
		OldTarget:  fullTarget(),
		NewTarget:  fullTarget(),
		Distance:   2,
	}
	got := applyChangedTargetOutputConfig(src, nil)
	require.NotNil(t, got)
	assert.Equal(t, pb.CHANGE_TYPE_CHANGED, got.GetChangeType(), "change type preserved")
	assert.Equal(t, int32(2), got.GetDistance(), "distance preserved")
	assert.Equal(t, "", got.GetOldTarget().GetHash())
	assert.Equal(t, "", got.GetNewTarget().GetHash())
	// Source unchanged.
	assert.Equal(t, "h1", src.GetOldTarget().GetHash())
}

func TestApplyOptimizedTargetsOutputConfigToChunk_StripsTargets(t *testing.T) {
	chunk := &pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Targets{
			Targets: &pb.OptimizedTargets{Targets: []*pb.OptimizedTarget{fullTarget(), fullTarget()}},
		},
	}
	got := applyOptimizedTargetsOutputConfigToChunk(chunk, nil)
	require.NotNil(t, got)
	for _, target := range got.GetTargets().GetTargets() {
		assert.Equal(t, "", target.GetHash())
		assert.Nil(t, target.GetTags())
		assert.Nil(t, target.GetAttributes())
	}
}

func TestApplyMetadataOutputConfig_NilConfigDropsTagAndAttrMappings(t *testing.T) {
	src := &pb.Metadata{
		TargetIdMapping:             map[int32]string{1: "//foo"},
		RuleTypeMapping:             map[int32]string{1: "go_library"},
		TagMapping:                  map[int32]string{1: "manual"},
		AttributeNameMapping:        map[int32]string{1: "visibility"},
		AttributeStringValueMapping: map[int32]string{1: "//visibility:public"},
	}
	got := applyMetadataOutputConfig(src, nil)
	require.NotNil(t, got)
	assert.Equal(t, map[int32]string{1: "//foo"}, got.GetTargetIdMapping())
	assert.Equal(t, map[int32]string{1: "go_library"}, got.GetRuleTypeMapping())
	assert.Nil(t, got.GetTagMapping())
	assert.Nil(t, got.GetAttributeNameMapping())
	assert.Nil(t, got.GetAttributeStringValueMapping())
	// Source unchanged.
	assert.NotNil(t, src.GetTagMapping())
}

func TestApplyMetadataOutputConfig_TagsOnlyKeepsTags(t *testing.T) {
	src := &pb.Metadata{
		TagMapping:                  map[int32]string{1: "manual"},
		AttributeNameMapping:        map[int32]string{1: "visibility"},
		AttributeStringValueMapping: map[int32]string{1: "//visibility:public"},
	}
	got := applyMetadataOutputConfig(src, &pb.OutputConfig{IncludeTags: true})
	require.NotNil(t, got)
	assert.Equal(t, map[int32]string{1: "manual"}, got.GetTagMapping())
	assert.Nil(t, got.GetAttributeNameMapping())
	assert.Nil(t, got.GetAttributeStringValueMapping())
}

func TestApplyMetadataOutputConfig_AttrsOnlyKeepsAttrs(t *testing.T) {
	src := &pb.Metadata{
		TagMapping:                  map[int32]string{1: "manual"},
		AttributeNameMapping:        map[int32]string{1: "visibility"},
		AttributeStringValueMapping: map[int32]string{1: "//visibility:public"},
	}
	got := applyMetadataOutputConfig(src, &pb.OutputConfig{IncludeAttributes: true})
	require.NotNil(t, got)
	assert.Nil(t, got.GetTagMapping())
	assert.Equal(t, map[int32]string{1: "visibility"}, got.GetAttributeNameMapping())
	assert.Equal(t, map[int32]string{1: "//visibility:public"}, got.GetAttributeStringValueMapping())
}

func TestApplyMetadataOutputConfig_AllIncludesPassesThrough(t *testing.T) {
	src := &pb.Metadata{TagMapping: map[int32]string{1: "manual"}}
	cfg := &pb.OutputConfig{IncludeTags: true, IncludeAttributes: true}
	got := applyMetadataOutputConfig(src, cfg)
	assert.Same(t, src, got, "no copy when nothing needs pruning (include_hashes irrelevant for metadata)")
}

func TestApplyOptimizedTargetsOutputConfigToChunk_MetadataTagAndAttrMappingsCleared(t *testing.T) {
	chunk := &pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Metadata{
			Metadata: &pb.Metadata{
				TargetIdMapping:             map[int32]string{1: "//foo"},
				RuleTypeMapping:             map[int32]string{1: "go_library"},
				TagMapping:                  map[int32]string{1: "manual"},
				AttributeNameMapping:        map[int32]string{1: "visibility"},
				AttributeStringValueMapping: map[int32]string{1: "//visibility:public"},
			},
		},
	}
	got := applyOptimizedTargetsOutputConfigToChunk(chunk, nil)
	require.NotNil(t, got)
	m := got.GetMetadata()
	// target_id and rule_type mappings preserved — still referenced by surviving fields.
	assert.Equal(t, map[int32]string{1: "//foo"}, m.GetTargetIdMapping())
	assert.Equal(t, map[int32]string{1: "go_library"}, m.GetRuleTypeMapping())
	// Tag and attribute mappings dropped — nothing references them after per-target stripping.
	assert.Nil(t, m.GetTagMapping())
	assert.Nil(t, m.GetAttributeNameMapping())
	assert.Nil(t, m.GetAttributeStringValueMapping())
}

func TestApplyOptimizedTargetsOutputConfigToChunk_FullIncludePassesThrough(t *testing.T) {
	chunk := &pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Targets{
			Targets: &pb.OptimizedTargets{Targets: []*pb.OptimizedTarget{fullTarget()}},
		},
	}
	cfg := &pb.OutputConfig{IncludeHashes: true, IncludeTags: true, IncludeAttributes: true}
	got := applyOptimizedTargetsOutputConfigToChunk(chunk, cfg)
	assert.Same(t, chunk, got, "no copy when nothing needs stripping")
}
