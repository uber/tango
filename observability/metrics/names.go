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

package metrics

// Operation names. Internal-only ops should be declared next to their emit
// site rather than added here.
const (
	OpGetTargetGraph        = "get_target_graph"
	OpGetChangedTargets     = "get_changed_targets"
	OpGetChangedTargetGraph = "get_changed_target_graph"
	OpGetGraph              = "get_graph"
	OpCompareTargetGraphs   = "compare_target_graphs"
	OpNativeOrchestrator    = "native_orchestrator"
	OpGraphRunner           = "graph_runner"
)

// Metric names.
const (
	Requests                         = "requests"
	TreehashCacheLookup              = "treehash_cache_lookup"
	GraphCacheLookup                 = "graph_cache_lookup"
	ChangedTargetsCacheLookup        = "changed_targets_cache_lookup"
	GraphCacheFetchDuration          = "graph_cache_fetch_duration"
	ChangedTargetsCacheFetchDuration = "changed_targets_cache_fetch_duration"
	CompareDuration                  = "compare_duration"
	ChangedTargetsCount              = "changed_targets_count"
	TotalDuration                    = "total_duration"
	Patch                            = "patch"
	PatchDuration                    = "patch_duration"
	BazelQueryDuration               = "bazel_query_duration"
	GitFileHashesDuration            = "git_file_hashes_duration"
	TargetHashDuration               = "target_hash_duration"
	TargetsCount                     = "targets_count"
	StorageUploadDuration            = "storage_upload_duration"
	InFlightRequests                 = "in_flight_requests"
	WorkspacesInFlight               = "in_flight_workspaces"
)

// Tag keys.
const (
	TagRepo          = "repo"
	TagEmitter       = "emitter"
	TagResult        = "result"
	TagOperation     = "operation"
	TagFailureType   = "failure_type"
	TagFailureReason = "failure_reason"
)

// Result is the value type for TagResult.
type Result string

// Result values for TagResult.
const (
	ResultUnknown Result = "unknown"
	ResultSuccess Result = "success"
	ResultFail    Result = "fail"
	ResultHit     Result = "hit"
	ResultMiss    Result = "miss"
)
