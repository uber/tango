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
	pb "github.com/uber/tango/tangopb"
)

// optimizedTargetNeedsStripping reports whether any per-target field would be
// removed by applyOptimizedTargetOutputConfig under cfg. Used to skip the
// shallow-copy when nothing would change.
func optimizedTargetNeedsStripping(cfg *pb.OutputConfig) bool {
	if cfg == nil {
		// nil OutputConfig means all three include_* flags default to false → strip all three.
		return true
	}
	return !cfg.GetIncludeHashes() || !cfg.GetIncludeTags() || !cfg.GetIncludeAttributes()
}

// applyOptimizedTargetOutputConfig returns a copy of src with hash, tags, or
// attributes cleared per cfg's include_* flags. Returns src unchanged when
// nothing needs stripping. Used to honor OutputConfig at send time so the
// cache can store the fully-populated payload once and serve any flag combo.
func applyOptimizedTargetOutputConfig(src *pb.OptimizedTarget, cfg *pb.OutputConfig) *pb.OptimizedTarget {
	if src == nil || !optimizedTargetNeedsStripping(cfg) {
		return src
	}
	dst := *src
	if cfg == nil || !cfg.GetIncludeHashes() {
		dst.Hash = ""
	}
	if cfg == nil || !cfg.GetIncludeTags() {
		dst.Tags = nil
	}
	if cfg == nil || !cfg.GetIncludeAttributes() {
		dst.Attributes = nil
	}
	return &dst
}

// applyOptimizedTargetsOutputConfig returns a slice with each element filtered
// per cfg. Returns the original slice unchanged when no stripping is needed.
func applyOptimizedTargetsOutputConfig(src []*pb.OptimizedTarget, cfg *pb.OutputConfig) []*pb.OptimizedTarget {
	if !optimizedTargetNeedsStripping(cfg) || len(src) == 0 {
		return src
	}
	out := make([]*pb.OptimizedTarget, len(src))
	for i, t := range src {
		out[i] = applyOptimizedTargetOutputConfig(t, cfg)
	}
	return out
}

// applyChangedTargetOutputConfig returns a copy of src with OldTarget and
// NewTarget filtered per cfg. Returns src unchanged when no stripping is needed.
func applyChangedTargetOutputConfig(src *pb.ChangedTarget, cfg *pb.OutputConfig) *pb.ChangedTarget {
	if src == nil || !optimizedTargetNeedsStripping(cfg) {
		return src
	}
	dst := *src
	dst.OldTarget = applyOptimizedTargetOutputConfig(src.GetOldTarget(), cfg)
	dst.NewTarget = applyOptimizedTargetOutputConfig(src.GetNewTarget(), cfg)
	return &dst
}

// applyChangedTargetsOutputConfig returns a slice with each element filtered
// per cfg. Returns the original slice unchanged when no stripping is needed.
func applyChangedTargetsOutputConfig(src []*pb.ChangedTarget, cfg *pb.OutputConfig) []*pb.ChangedTarget {
	if !optimizedTargetNeedsStripping(cfg) || len(src) == 0 {
		return src
	}
	out := make([]*pb.ChangedTarget, len(src))
	for i, ct := range src {
		out[i] = applyChangedTargetOutputConfig(ct, cfg)
	}
	return out
}

// applyOptimizedTargetsOutputConfigToChunk returns a copy of chunk with its
// OptimizedTargets payload filtered per cfg. Non-targets chunks (Metadata)
// and chunks that need no stripping are returned unchanged.
func applyOptimizedTargetsOutputConfigToChunk(chunk *pb.GetTargetGraphResponse, cfg *pb.OutputConfig) *pb.GetTargetGraphResponse {
	if chunk == nil {
		return chunk
	}
	switch item := chunk.GetItem().(type) {
	case *pb.GetTargetGraphResponse_Targets:
		if !optimizedTargetNeedsStripping(cfg) || item.Targets == nil {
			return chunk
		}
		filtered := applyOptimizedTargetsOutputConfig(item.Targets.GetTargets(), cfg)
		return &pb.GetTargetGraphResponse{
			Item: &pb.GetTargetGraphResponse_Targets{
				Targets: &pb.OptimizedTargets{Targets: filtered},
			},
		}
	case *pb.GetTargetGraphResponse_Metadata:
		pruned := applyMetadataOutputConfig(item.Metadata, cfg)
		if pruned == item.Metadata {
			return chunk
		}
		return &pb.GetTargetGraphResponse{
			Item: &pb.GetTargetGraphResponse_Metadata{Metadata: pruned},
		}
	}
	return chunk
}

// metadataNeedsPruning reports whether applyMetadataOutputConfig would drop
// any mapping under cfg. Tag mapping is dropped when include_tags is off;
// attribute name + string value mappings are dropped when include_attributes
// is off. include_hashes has no metadata impact since hashes are inline.
func metadataNeedsPruning(cfg *pb.OutputConfig) bool {
	if cfg == nil {
		return true
	}
	return !cfg.GetIncludeTags() || !cfg.GetIncludeAttributes()
}

// applyMetadataOutputConfig returns a copy of meta with mappings cleared for
// fields stripped from per-target output, so the response doesn't ship dead
// ID->name tables. Returns meta unchanged when nothing needs pruning.
func applyMetadataOutputConfig(meta *pb.Metadata, cfg *pb.OutputConfig) *pb.Metadata {
	if meta == nil || !metadataNeedsPruning(cfg) {
		return meta
	}
	dst := *meta
	if cfg == nil || !cfg.GetIncludeTags() {
		dst.TagMapping = nil
	}
	if cfg == nil || !cfg.GetIncludeAttributes() {
		dst.AttributeNameMapping = nil
		dst.AttributeStringValueMapping = nil
	}
	return &dst
}
