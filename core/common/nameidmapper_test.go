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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNameIDMapper_AssignsSequentialAndStableIDs(t *testing.T) {
	mapper := NewNameIDMapper()

	idA := mapper.ID("a")
	assert.Equal(t, int32(1), idA, "first id must be 1; 0 is reserved as proto3 unspecified")
	idB := mapper.ID("b")
	assert.Equal(t, int32(2), idB)
	// Re-requesting 'a' should return the same id
	idA2 := mapper.ID("a")
	assert.Equal(t, idA, idA2)
	// A new, third name should get the next sequential id
	idC := mapper.ID("c")
	assert.Equal(t, int32(3), idC)
}

func TestNameIDMapper_Invert(t *testing.T) {
	mapper := NewNameIDMapper()
	names := []string{"x", "y", "z"}
	for _, n := range names {
		mapper.ID(n)
	}

	inv := mapper.Invert()
	assert.Equal(t, 3, len(inv))
	assert.Equal(t, "x", inv[1])
	assert.Equal(t, "y", inv[2])
	assert.Equal(t, "z", inv[3])
	_, hasZero := inv[0]
	assert.False(t, hasZero, "id 0 must remain reserved")

	// Mutating the returned map must not affect the mapper's internal state.
	inv[4] = "extra"
	fresh := mapper.Invert()
	_, ok := fresh[4]
	assert.False(t, ok)
}

func TestNameIDMapper_NeverAssignsZero(t *testing.T) {
	mapper := NewNameIDMapper()
	for i := 0; i < 1000; i++ {
		id := mapper.ID(fmt.Sprintf("name-%d", i))
		assert.NotEqual(t, int32(0), id)
	}
}
