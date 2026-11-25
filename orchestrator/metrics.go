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

package orchestrator

// opGetTargetGraph is snake_cased after the Orchestrator.GetTargetGraph method.
const _opGetTargetGraph = "get_target_graph"

// _opTreehashCacheWrite tracks treehash mapping upload that runs before graph computation.
const _opTreehashCacheWrite = "treehash_cache_write"

const (
	// _metricITGApplicable counts requests for which the incremental target
	// graph was attempted: the repository enables it and the request is eligible.
	_metricITGApplicable = "itg_applicable"

	// _metricITGComputeDuration is how long the incremental attempt took,
	// tagged result=success, or result=fallback when the full query ran instead.
	_metricITGComputeDuration = "itg_compute_duration"

	// _resultFallback is the result tag value for an ITG attempt that did not
	// produce a graph.
	_resultFallback = "fallback"
)
