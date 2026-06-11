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

// filterChangedTargetsByDistance returns targets where 0 <= distance <= maxDist.
// Returns the input slice unchanged when maxDist < 0 (filtering disabled).
func filterChangedTargetsByDistance(targets []*pb.ChangedTarget, maxDist int32) []*pb.ChangedTarget {
	if maxDist < 0 {
		return targets
	}
	kept := make([]*pb.ChangedTarget, 0, len(targets))
	for _, t := range targets {
		if d := t.GetDistance(); d >= 0 && d <= maxDist {
			kept = append(kept, t)
		}
	}
	return kept
}
