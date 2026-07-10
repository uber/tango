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

// Tag keys.
const (
	TagRepo          = "repo"
	TagEmitter       = "emitter"
	TagResult        = "result"
	TagOperation     = "operation"
	TagFailureType   = "failure_type"
	TagFailureSource = "failure_source"
)

// Result is the value type for TagResult.
type Result string

// Result values for TagResult.
const (
	ResultSuccess Result = "success"
	ResultFail    Result = "fail"
	ResultHit     Result = "hit"
	ResultMiss    Result = "miss"
)
