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
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/observability/metrics"
)

// failure_reason tag values for errors that originate in the controller itself.
// Errors from the orchestrator carry their own reason via common.ClassifiedError.
// Shared reasons live in core/common as common.FailureReason*.
const (
	// Reading a cached target graph from storage failed.
	failureReasonGraphFetch = "graph_fetch"
	// Streaming a response message back to the client failed.
	failureReasonSend = "send"
	// Diffing two target graphs failed.
	failureReasonCompare = "compare"
	// Reading a stored treehash from storage failed (not a cache miss).
	failureReasonTreehashRead = "treehash_read"
)

// emitFailureMetric tags the failure counter with err's ErrorCode. e should
// already carry the repo tag; op is the operation subscope the counter lands under.
func emitFailureMetric(e *metrics.Emitter, op string, err error) {
	e.Tagged(map[string]string{
		"error_code": tangoerrors.GetErrorCode(err).String(),
	}).Counter(op, "failures").Inc(1)
}
