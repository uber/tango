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
	"github.com/uber/tango/core/common"
	pb "github.com/uber/tango/tangopb"
)

// idMappers bundles the five canonical name->ID mappers used to assemble a
// per-call output namespace. Keeping them together avoids passing five
// arguments through every transposition.
type idMappers struct {
	targets   *common.NameIDMapper
	ruleTypes *common.NameIDMapper
	tags      *common.NameIDMapper
	attrNames *common.NameIDMapper
	attrVals  *common.NameIDMapper
}

func newIDMappers() *idMappers {
	return &idMappers{
		targets:   common.NewNameIDMapper(),
		ruleTypes: common.NewNameIDMapper(),
		tags:      common.NewNameIDMapper(),
		attrNames: common.NewNameIDMapper(),
		attrVals:  common.NewNameIDMapper(),
	}
}

// transpose remaps src into the canonical ID space using the source
// metadata's name lookups. Returns nil when src is nil.
func (m *idMappers) transpose(src *pb.OptimizedTarget, meta *pb.Metadata) *pb.OptimizedTarget {
	if src == nil {
		return nil
	}
	return transposeOptimizedTarget(
		src,
		meta.GetTargetIdMapping(),
		meta.GetRuleTypeMapping(),
		meta.GetTagMapping(),
		meta.GetAttributeNameMapping(),
		meta.GetAttributeStringValueMapping(),
		m.targets.ID, m.ruleTypes.ID, m.tags.ID, m.attrNames.ID, m.attrVals.ID,
	)
}

// chunkMetadata produces canonical metadata chunks from the mapper state,
// sized by chunkSize. Callers wrap each entry in the appropriate response
// envelope.
func (m *idMappers) chunkMetadata(chunkSize int) []*pb.Metadata {
	return common.ChunkMetadata(
		m.targets.Invert(),
		m.ruleTypes.Invert(),
		m.tags.Invert(),
		m.attrNames.Invert(),
		m.attrVals.Invert(),
		chunkSize,
	)
}
