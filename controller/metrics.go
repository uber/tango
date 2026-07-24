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
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/observability/metrics"
)

// Operation names, snake_cased after the RPC interface methods they measure.
const (
	opGetTargetGraph        = "get_target_graph"
	opGetChangedTargets     = "get_changed_targets"
	opGetChangedTargetGraph = "get_changed_target_graph"
	opCompareTargetGraphs   = "compare_target_graphs"
)

// Cache-lookup metric names. Each is emitted under its parent RPC op with a
// result=hit|miss tag, so a hit rate is derivable per cache layer.
const (
	_metricTreehashCacheLookup        = "treehash_cache_lookup"
	_metricGraphCacheLookup           = "graph_cache_lookup"
	_metricComparedTargetsCacheLookup = "compared_targets_cache_lookup"
)

// recordCacheLookup emits a result-tagged counter for a cache lookup under the
// given parent op: a nil error is a hit and a not-found error is a miss. Any
// other error is an infra failure (already tracked by the failure metric), so
// nothing is emitted — an infra error is not a cache miss and must not skew the
// hit rate.
func recordCacheLookup(e *metrics.Emitter, parentOp, name string, err error) {
	var result string
	switch {
	case err == nil:
		result = metrics.ResultHit
	case storage.IsNotFound(err):
		result = metrics.ResultMiss
	default:
		return
	}
	e.Tagged(map[string]string{metrics.TagResult: result}).Counter(parentOp, name).Inc(1)
}
