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

package common

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/targethasher"
	pb "github.com/uber/tango/tangopb"
)

func TestChunkTargets(t *testing.T) {
	t.Parallel()

	// Create 25 targets, chunk by 10 → expect 3 chunks (10, 10, 5)
	targets := make([]*pb.OptimizedTarget, 25)
	for i := range targets {
		targets[i] = &pb.OptimizedTarget{Id: int32(i)}
	}

	responses := chunkTargets(targets, 10)

	require.Len(t, responses, 3)

	// Verify total count and order preserved
	var total int
	for _, resp := range responses {
		item := resp.Item.(*pb.GetTargetGraphResponse_Targets)
		for _, target := range item.Targets.Targets {
			assert.Equal(t, int32(total), target.Id)
			total++
		}
	}
	assert.Equal(t, 25, total)
}

func TestResultToGetTargetGraphResponse_Chunking(t *testing.T) {
	t.Parallel()

	numTargets := 50
	result := targethasher.Result{
		TargetNames: make([]string, numTargets),
		Targets:     make(map[string]*targethasher.Target, numTargets),
	}
	for i := 0; i < numTargets; i++ {
		name := fmt.Sprintf("//pkg:target%d", i)
		result.TargetNames[i] = name
		result.Targets[name] = &targethasher.Target{Name: name, Hash: []byte{0}, RuleType: "go_library"}
	}

	tests := []struct {
		name                 string
		targetChunkSize      int
		metadataMapChunkSize int
		wantTargetChunks     int
		wantMetadataChunks   int
	}{
		{
			name:                 "25 per chunk",
			targetChunkSize:      25,
			metadataMapChunkSize: 20,
			wantTargetChunks:     2,
			wantMetadataChunks:   3,
		},
		{
			name:                 "10 per chunk",
			targetChunkSize:      10,
			metadataMapChunkSize: 10,
			wantTargetChunks:     5,
			wantMetadataChunks:   5,
		},
		{
			name:                 "all in one chunk",
			targetChunkSize:      100,
			metadataMapChunkSize: 5_000,
			wantTargetChunks:     1,
			wantMetadataChunks:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			responses, err := ResultToGetTargetGraphResponse(context.Background(), result, tt.targetChunkSize, tt.metadataMapChunkSize)
			require.NoError(t, err)

			var targetChunks, metadataChunks int
			for _, resp := range responses {
				switch resp.Item.(type) {
				case *pb.GetTargetGraphResponse_Targets:
					targetChunks++
				case *pb.GetTargetGraphResponse_Metadata:
					metadataChunks++
				}
			}
			assert.Equal(t, tt.wantTargetChunks, targetChunks)
			assert.Equal(t, tt.wantMetadataChunks, metadataChunks)
		})
	}
}
