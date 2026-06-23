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
	"context"
	"errors"

	"github.com/uber-go/tally"
	"github.com/uber/tango/core/common"
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

// emitFailureMetric tags the failure counter with the reason and type from the
// error's ClassifiedError. Context errors are recognised explicitly; everything
// else falls back to unknown/infra.
func emitFailureMetric(scope tally.Scope, err error) {
	var ce common.ClassifiedError
	switch {
	case errors.As(err, &ce):
		// already classified — use the error's own reason and type
	case errors.Is(err, context.Canceled):
		ce = common.WithReason(common.FailureReasonCancelled, common.ErrorTypeUser, err)
	case errors.Is(err, context.DeadlineExceeded):
		ce = common.WithReason(common.FailureReasonDeadlineExceeded, common.ErrorTypeUser, err)
	default:
		ce = common.WithReason(common.FailureReasonUnknown, common.ErrorTypeInfra, err)
	}
	scope.Tagged(map[string]string{
		"failure_type":   ce.Type(),
		"failure_reason": ce.Reason(),
	}).Counter("failure_type").Inc(1)
}
