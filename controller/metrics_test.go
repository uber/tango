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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber-go/tally"
	"github.com/uber/tango/observability/metrics"
	"go.uber.org/zap"
)

// TestControllerMetricsPathShape drives the stub GetChangedTargetGraph handler
// through a TestScope-backed emitter and asserts the start/finish lifecycle
// lands under <controller-scope>.<op>.<metric> with the outcome tag.
// The handler returns Unimplemented (classified as an infrastructure error),
// so the finish histogram carries result=infra rather than result=success.
func TestControllerMetricsPathShape(t *testing.T) {
	ts := tally.NewTestScope("controller", nil)
	e := metrics.New(ts)

	c := &controller{logger: zap.NewNop(), emitter: e, appCtx: context.Background()}
	require.Error(t, c.GetChangedTargetGraph(nil, nil))

	snap := ts.Snapshot()
	assert.Contains(t, snap.Counters(), "controller.get_changed_target_graph.start+")
	assert.Contains(t, snap.Histograms(), "controller.get_changed_target_graph.finish+result=infra")
}
