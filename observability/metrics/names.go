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

import (
	tangoerrors "github.com/uber/tango/core/errors"
)

// Operation (op) names live in each consuming package's metrics.go, named after
// the interface method they measure (e.g. "GetTargetGraph", "Compute").

// Tag keys.
const (
	TagRepo   = "repo"
	TagResult = "result"
)

// Result values for TagResult.
const (
	ResultSuccess = "success"
	ResultHit     = "hit"
	ResultMiss    = "miss"
)

// Outcome maps an error to a result tag value. A nil error is "success";
// any non-nil error delegates to tangoerrors.GetErrorCode, which returns
// "cancelled", "user", "infra", or "infra_retryable".
func Outcome(err error) string {
	if err == nil {
		return ResultSuccess
	}
	return tangoerrors.GetErrorCode(err).String()
}
