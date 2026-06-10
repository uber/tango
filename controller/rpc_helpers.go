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
	"errors"

	"github.com/uber-go/tally"
	pb "github.com/uber/tango/tangopb"
)

// recordRPCResult is the success/failure accounting block every RPC defers.
// On error it increments the "failure" counter and emits a classified
// failure_type metric; on success it increments "success".
func recordRPCResult(scope tally.Scope, errPtr *error) {
	if errPtr != nil && *errPtr != nil {
		scope.Counter("failure").Inc(1)
		emitFailureMetric(scope, *errPtr)
		return
	}
	scope.Counter("success").Inc(1)
}

// validateRevisionPair enforces the minimal invariants the comparison
// pipeline relies on: both revisions present, both populated with a remote
// and base SHA, and both pointing at the same remote.
func validateRevisionPair(first, second *pb.BuildDescription) error {
	if first == nil {
		return errors.New("first revision is required")
	}
	if second == nil {
		return errors.New("second revision is required")
	}
	if first.GetRemote() == "" {
		return errors.New("first revision remote is required")
	}
	if first.GetBaseSha() == "" {
		return errors.New("first revision base_sha is required")
	}
	if second.GetRemote() == "" {
		return errors.New("second revision remote is required")
	}
	if second.GetBaseSha() == "" {
		return errors.New("second revision base_sha is required")
	}
	if first.GetRemote() != second.GetRemote() {
		return errors.New("first and second revision must have the same remote")
	}
	return nil
}
