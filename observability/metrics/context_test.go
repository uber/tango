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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber-go/tally"
)

func TestFromContextEmptyReturnsNoop(t *testing.T) {
	got := FromContext(context.Background())
	require.NotNil(t, got)
	got.Inc("op", "requests")
	got.Gauge("op", "in_flight_requests", 1)
}

func TestFromContextNilCtxReturnsNoop(t *testing.T) {
	//nolint:staticcheck // deliberately testing nil context handling
	got := FromContext(nil)
	require.NotNil(t, got)
	got.Inc("op", "requests")
}

func TestWithEmitterRoundTrip(t *testing.T) {
	s := tally.NewTestScope("", nil)
	e := New(s).Tagged(map[string]string{TagRepo: "test"})
	ctx := WithEmitter(context.Background(), e)
	got := FromContext(ctx)
	assert.Same(t, e, got)

	got.Inc(OpGetTargetGraph, Requests)
	assert.Equal(t, int64(1), counterValue(t, s, OpGetTargetGraph+"."+Requests, map[string]string{
		TagRepo: "test",
	}))
}

func TestWithEmitterNilStoredValueReturnsNoop(t *testing.T) {
	ctx := context.WithValue(context.Background(), emitterKey{}, (*Emitter)(nil))
	got := FromContext(ctx)
	require.NotNil(t, got)
	got.Inc("op", "requests")
}
