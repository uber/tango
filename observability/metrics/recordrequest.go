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

import "time"

// RecordRequest records the request's TotalDuration and emits a requests
// counter tagged with the result: result=success when err is nil, otherwise
// result=fail.
func RecordRequest(e *Emitter, op string, dur time.Duration, err error) {
	e.RecordDur(op, TotalDuration, dur)

	result := ResultSuccess
	if err != nil {
		result = ResultFail
	}
	// TODO: when err != nil, derive failure_type and failure_source tags from
	// err via an error classifier (added in a later change) and attach them here.
	e.Inc(op, Requests, WithTags(map[string]string{
		TagResult: string(result),
	}))
}
