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
	pb "github.com/uber/tango/tangopb"
)

func (c *controller) GetChangedTargetGraph(request *pb.GetChangedTargetGraphRequest, stream pb.TangoServiceGetChangedTargetGraphYARPCServer) (retErr error) {
	scope := c.scope.SubScope("get_changed_target_graph")
	defer func() {
		if retErr != nil {
			scope.Counter("failure").Inc(1)
		} else {
			scope.Counter("success").Inc(1)
		}
	}()
	return nil
}
