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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/uber-go/tally"
)

func TestRecordRequestSuccess(t *testing.T) {
	s := tally.NewTestScope("", nil)
	RecordRequest(New(s), OpGetTargetGraph, 5*time.Millisecond, nil)

	assert.Equal(t, int64(1), counterValue(t, s, OpGetTargetGraph+"."+Requests, map[string]string{
		TagResult: string(ResultSuccess),
	}))
	assert.Equal(t, 1, histogramDurationSamples(t, s, OpGetTargetGraph+"."+TotalDuration, map[string]string{}))
}

func TestRecordRequestFailure(t *testing.T) {
	s := tally.NewTestScope("", nil)
	RecordRequest(New(s), OpGetTargetGraph, time.Millisecond, errors.New("boom"))

	assert.Equal(t, int64(1), counterValue(t, s, OpGetTargetGraph+"."+Requests, map[string]string{
		TagResult: string(ResultFail),
	}))
}
