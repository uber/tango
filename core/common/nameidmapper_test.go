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
	"testing"
	"github.com/stretchr/testify/assert"
)

func TestNameIDMapper_AssignsSequentialAndStableIDs(t *testing.T) {
	mapper := NewNameIDMapper()

	idA := mapper.ID("a")
	assert.Equal(t, int32(0), idA)
	idB := mapper.ID("b")
	assert.Equal(t, int32(1), idB)
	// Re-requesting 'a' should return the same id
	idA2 := mapper.ID("a")
	assert.Equal(t, idA, idA2)
	// A new, third name should get the next sequential id
	idC := mapper.ID("c")
	assert.Equal(t, int32(2), idC)
}

func TestNameIDMapper_MappingAndInvert(t *testing.T) {
	mapper := NewNameIDMapper()
	names := []string{"x", "y", "z"}
	for _, n := range names {
		mapper.ID(n)
	}

	mapping := mapper.Mapping()
	assert.Equal(t, 3, len(mapping))
	assert.Equal(t, int32(0), mapping["x"])
	assert.Equal(t, int32(1), mapping["y"])
	assert.Equal(t, int32(2), mapping["z"])

	inv := mapper.Invert()
	assert.Equal(t, 3, len(inv))
	assert.Equal(t, "x", inv[0])
	assert.Equal(t, "y", inv[1])
	assert.Equal(t, "z", inv[2])

	// Mutating the inverted map should not affect the original mapping
	inv[3] = "extra"
	_, ok := mapping["extra"]
	assert.False(t, ok)
}
