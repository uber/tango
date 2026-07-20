package entity

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOptimizedTarget_Size(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		target   OptimizedTarget
		wantSize int
	}{
		{
			name:     "zero value",
			target:   OptimizedTarget{},
			wantSize: 0,
		},
		{
			name:   "id only",
			target: OptimizedTarget{ID: 1},
			// field tag (1 byte) + varint(1) (1 byte) = 2
			wantSize: 2,
		},
		{
			name:   "with hash",
			target: OptimizedTarget{ID: 42, Hash: "abcd"},
			// id: tag(1) + varint(42)(1) = 2
			// hash: tag(1) + len_prefix(1) + 4 bytes = 6
			wantSize: 8,
		},
		{
			name:   "with deps packed",
			target: OptimizedTarget{ID: 1, DirectDependencies: []int32{2, 3}},
			// id: tag(1) + varint(1)(1) = 2
			// deps packed: tag(1) + len_prefix(1) + varint(2)(1) + varint(3)(1) = 4
			wantSize: 6,
		},
		{
			name:   "booleans",
			target: OptimizedTarget{ID: 1, Root: true, External: true},
			// id: 2, root: tag(1) + 1 = 2, external: tag(1) + 1 = 2
			wantSize: 6,
		},
		{
			name:     "false booleans omitted",
			target:   OptimizedTarget{ID: 1, Root: false, External: false},
			wantSize: 2,
		},
		{
			name:   "with attributes",
			target: OptimizedTarget{ID: 1, Attributes: map[int32]int32{1: 2}},
			// id: 2
			// map entry: tag(1) + len_prefix(1) + [key: tag(1)+varint(1)(1) + val: tag(1)+varint(2)(1)] = 6
			wantSize: 8,
		},
		{
			name:   "tags packed",
			target: OptimizedTarget{Tags: []int32{5, 6}},
			// tags packed: tag(1) + len_prefix(1) + varint(5)(1) + varint(6)(1) = 4
			wantSize: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.wantSize, tt.target.Size())
		})
	}
}
