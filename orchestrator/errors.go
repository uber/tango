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

package orchestrator

// failure_reason tag values emitted by the orchestrator.
// Shared reasons live in core/common as common.FailureReason*.
const (
	failureReasonConfigParse       = "config_parse"
	failureReasonNoRepoConfig      = "no_repo_config"
	failureReasonWorkspaceLease    = "workspace_lease"
	failureReasonWorkspaceCheckout = "workspace_checkout"
	failureReasonRequestCreate     = "request_create"
	failureReasonRequestApply      = "request_apply"
	failureReasonTreehashCompute   = "treehash_compute"
	failureReasonBazelClient       = "bazel_client"
	failureReasonGraphCompute      = "graph_compute"
	failureReasonGraphConvert      = "graph_convert"
)
