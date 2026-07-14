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

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestChunkSizesForByteBudget(t *testing.T) {
	tests := []struct {
		name                       string
		giveMaxMessageBytes        int
		wantTargetChunkSize        int
		wantChangedTargetChunkSize int
		wantMetadataMapChunkSize   int
	}{
		{
			name:                       "non-positive falls back to default budget",
			giveMaxMessageBytes:        0,
			wantTargetChunkSize:        106,
			wantChangedTargetChunkSize: 53,
			wantMetadataMapChunkSize:   50_000,
		},
		{
			name:                       "negative falls back to default budget",
			giveMaxMessageBytes:        -1,
			wantTargetChunkSize:        106,
			wantChangedTargetChunkSize: 53,
			wantMetadataMapChunkSize:   50_000,
		},
		{
			name:                       "custom budget scales proportionally",
			giveMaxMessageBytes:        8_500_000,
			wantTargetChunkSize:        212,
			wantChangedTargetChunkSize: 106,
			wantMetadataMapChunkSize:   100_000,
		},
		{
			name:                       "tiny budget never returns zero",
			giveMaxMessageBytes:        1,
			wantTargetChunkSize:        1,
			wantChangedTargetChunkSize: 1,
			wantMetadataMapChunkSize:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targetChunkSize, changedTargetChunkSize, metadataMapChunkSize := ChunkSizesForByteBudget(tt.giveMaxMessageBytes)
			assert.Equal(t, tt.wantTargetChunkSize, targetChunkSize)
			assert.Equal(t, tt.wantChangedTargetChunkSize, changedTargetChunkSize)
			assert.Equal(t, tt.wantMetadataMapChunkSize, metadataMapChunkSize)
		})
	}
}
