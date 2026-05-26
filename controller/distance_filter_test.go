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
	"testing"

	"github.com/stretchr/testify/assert"
	pb "github.com/uber/tango/tangopb"
)

func TestMaxDistanceFromOutputConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  *pb.OutputConfig
		want int32
	}{
		{"nil config disables filter", nil, -1},
		{"compute_distances unset disables filter", &pb.OutputConfig{}, -1},
		{"compute_distances unset ignores max_distance", &pb.OutputConfig{MaxDistance: 5}, -1},
		{"compute_distances true returns max", &pb.OutputConfig{ComputeDistances: true, MaxDistance: 5}, 5},
		{"compute_distances true returns 0", &pb.OutputConfig{ComputeDistances: true, MaxDistance: 0}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, maxDistanceFromOutputConfig(tt.cfg))
		})
	}
}

func TestFilterChangedTargetsByDistance(t *testing.T) {
	targets := []*pb.ChangedTarget{
		{Distance: 0},
		{Distance: 1},
		{Distance: 2},
		{Distance: 5},
		{Distance: -1},
	}

	t.Run("negative maxDist returns input unchanged", func(t *testing.T) {
		got := filterChangedTargetsByDistance(targets, -1)
		assert.Equal(t, targets, got, "should return original slice when filtering disabled")
	})

	t.Run("maxDist=0 keeps only distance-0", func(t *testing.T) {
		got := filterChangedTargetsByDistance(targets, 0)
		assert.Len(t, got, 1)
		assert.Equal(t, int32(0), got[0].GetDistance())
	})

	t.Run("maxDist=1 keeps distance 0 and 1", func(t *testing.T) {
		got := filterChangedTargetsByDistance(targets, 1)
		assert.Len(t, got, 2)
		for _, ct := range got {
			assert.True(t, ct.GetDistance() >= 0 && ct.GetDistance() <= 1)
		}
	})

	t.Run("negative-distance targets always dropped", func(t *testing.T) {
		got := filterChangedTargetsByDistance(targets, 100)
		assert.Len(t, got, 4, "distance -1 should be filtered even with large max")
		for _, ct := range got {
			assert.GreaterOrEqual(t, ct.GetDistance(), int32(0))
		}
	})

	t.Run("empty input returns empty output", func(t *testing.T) {
		got := filterChangedTargetsByDistance(nil, 5)
		assert.Empty(t, got)
	})
}
