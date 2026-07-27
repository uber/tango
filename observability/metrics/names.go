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
	"github.com/uber/tango/core/storage"
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

// Cache-lookup metric names. Each is emitted under its parent RPC op with a
// result=hit|miss tag, so a hit rate is derivable per cache layer.
const (
	TreehashCacheLookup        = "treehash_cache_lookup"
	GraphCacheLookup           = "graph_cache_lookup"
	ComparedTargetsCacheLookup = "compared_targets_cache_lookup"
)

// RecordCacheLookup emits a result-tagged counter for a cache lookup under the
// given parent op: a nil error is a hit and a not-found error is a miss. Any
// other error is an infra failure (already tracked by the failure metric), so
// nothing is emitted — an infra error is not a cache miss and must not skew the
// hit rate.
func RecordCacheLookup(e *Emitter, parentOp, name string, err error) {
	var result string
	switch {
	case err == nil:
		result = ResultHit
	case storage.IsNotFound(err):
		result = ResultMiss
	default:
		return
	}
	e.Tagged(map[string]string{TagResult: result}).Counter(parentOp, name).Inc(1)
}

// Outcome maps an error to a result tag value. A nil error is "success";
// any non-nil error delegates to tangoerrors.GetErrorCode, which returns
// "cancelled", "user", "infra", or "infra_retryable".
func Outcome(err error) string {
	if err == nil {
		return ResultSuccess
	}
	return tangoerrors.GetErrorCode(err).String()
}
