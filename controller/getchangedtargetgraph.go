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
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/yarpc/yarpcerrors"
)

// GetChangedTargetGraph is the streaming RPC that will return the subgraph
// induced by the changed targets between two revisions. NOT YET IMPLEMENTED:
// returns a YARPC Unimplemented error so callers get an explicit failure
// instead of a silent empty stream.
func (c *controller) GetChangedTargetGraph(request *pb.GetChangedTargetGraphRequest, stream pb.TangoServiceGetChangedTargetGraphYARPCServer) (retErr error) {
	op := metrics.Begin(c.emitter, opGetChangedTargetGraph, metrics.SlowDurationBuckets)
	retErr = tangoerrors.NewInfra(
		yarpcerrors.Newf(yarpcerrors.CodeUnimplemented, "GetChangedTargetGraph is not yet implemented"),
	)
	defer func() {
		op.Complete(retErr)
	}()
	return retErr
}
