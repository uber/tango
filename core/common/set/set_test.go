package set

import (
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPreallocate(t *testing.T) {
	// keep this function local so multiple test suites don't have name collisions
	size := 10
	s := NewCap[int](size)
	assert.Len(t, s, 0)
	// cannot actually assert cap() of a map
}

func TestSet(t *testing.T) {
	s := make(Set[int])
	assert.False(t, s.Contains(1))

	s.Insert(1)
	assert.True(t, s.Contains(1))
	assert.False(t, s.Contains(2))

	s.Insert(2)
	assert.True(t, s.Contains(1))
	assert.True(t, s.Contains(2))
	assert.False(t, s.Contains(3))

	s.Delete(1)
	assert.False(t, s.Contains(1))
	assert.True(t, s.Contains(2))
	assert.False(t, s.Contains(3))
}

func TestClear(t *testing.T) {
	size := 10
	s := NewCap[int](size)
	assert.Len(t, s, 0)
	s.InsertAll(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	assert.Len(t, s, 10)
	s.Clear()
	assert.False(t, s.Contains(1))
	assert.False(t, s.Contains(5))
	assert.False(t, s.Contains(10))
	assert.Len(t, s, 0)
}

func TestCheckedDelete(t *testing.T) {
	s := make(Set[int])
	s.Insert(1)

	assert.True(t, s.CheckedDelete(1))
	assert.False(t, s.CheckedDelete(1))
}

func TestDifference(t *testing.T) {
	set1 := make(Set[int])
	set2 := make(Set[int])

	// Difference between empty sets.
	assert.Empty(t, set1.Difference(set1))
	assert.Empty(t, set1.Difference(set2))

	// Simple difference
	set1.Insert(1)
	diff := set1.Difference(set2)
	assert.Len(t, diff, 1)
	assert.True(t, diff.Contains(1))

	// More complex difference
	set1.Insert(2)
	set2.Insert(2)
	set2.Insert(3)
	diff = set1.Difference(set2)
	assert.Len(t, diff, 1)
	assert.True(t, diff.Contains(1))
}

func TestSymDifference(t *testing.T) {
	set1 := make(Set[int])
	set2 := make(Set[int])

	// SymDifference between empty sets.
	assert.Empty(t, set1.SymDifference(set1))
	assert.Empty(t, set1.SymDifference(set2))

	// Simple symm difference
	set1.Insert(1)
	set1.Insert(2)
	set2.Insert(2)
	set2.Insert(3)
	diff := set1.SymDifference(set2)
	assert.Len(t, diff, 2)
	assert.True(t, diff.Contains(1))
	assert.True(t, diff.Contains(3))
}

func TestIntersection(t *testing.T) {
	tests := map[string]func(t *testing.T){
		"emtpy intersection": func(t *testing.T) {
			set1 := make(Set[int])
			set2 := make(Set[int])

			assert.Empty(t, set1.Intersection(set1))
			assert.Empty(t, set1.Intersection(set2))
			assert.Empty(t, set2.Intersection(set1))
			assert.Empty(t, set2.Intersection(set2))
		},
		"All elements the same": func(t *testing.T) {
			set1 := New[int](1, 2)
			set2 := New[int](2, 1)

			intersect := set1.Intersection(set2)
			assert.Len(t, intersect, 2)
			assert.True(t, intersect.Contains(1))
			assert.Equal(t, intersect, New[int](2, 1))
			assert.Equal(t, intersect, set2.Intersection(set1))
		},
		"Overlapping sets": func(t *testing.T) {
			set1 := New[int](2)
			set2 := New[int](2, 3)

			intersect := set1.Intersection(set2)
			assert.Len(t, intersect, 1)
			assert.True(t, intersect.Contains(2))
			assert.Equal(t, intersect, set2.Intersection(set1))
		},
		"disjoint sets": func(t *testing.T) {
			// Disjoint sets
			set1 := New[int](1)
			set2 := New[int](3)

			intersect := set1.Intersection(set2)
			assert.Empty(t, intersect)
			assert.Equal(t, intersect, set2.Intersection(set1))
		},
	}
	for name, tt := range tests {
		t.Run(name, tt)
	}
}

func TestUnsortedList(t *testing.T) {
	s := make(Set[int])
	assert.Empty(t, s.UnsortedList())

	s.Insert(1)
	expected := []int{1}
	assert.ElementsMatch(t, expected, s.UnsortedList())

	s.Insert(2)
	s.Insert(3)
	expected = []int{2, 3, 1}
	assert.ElementsMatch(t, expected, s.UnsortedList())
}

func TestNew(t *testing.T) {
	s := New[int](1, 2, 3)
	expected := []int{2, 3, 1}
	assert.ElementsMatch(t, expected, s.UnsortedList())
}

func TestInsertUnique(t *testing.T) {
	s := New[int]()
	assert.Len(t, s, 0)

	s.Insert(1)

	assert.False(t, s.InsertUnique(1))
	assert.True(t, s.InsertUnique(2))
	assert.False(t, s.InsertUnique(2))

	assert.Len(t, s, 2)
	expected := []int{2, 1}
	assert.ElementsMatch(t, expected, s.UnsortedList())
}

func TestEqual(t *testing.T) {
	a := make(Set[int])
	b := make(Set[int])
	assert.True(t, a.Equal(b))
	assert.True(t, b.Equal(a))

	a.Insert(1)
	assert.False(t, a.Equal(b))
	assert.False(t, b.Equal(a))

	b.Insert(2)
	assert.False(t, a.Equal(b))
	assert.False(t, b.Equal(a))

	b.Insert(1)
	assert.False(t, a.Equal(b))
	assert.False(t, b.Equal(a))

	a.Insert(2)
	assert.True(t, a.Equal(b))
	assert.True(t, b.Equal(a))
}

func TestContainsAll(t *testing.T) {
	a := make(Set[int])
	assert.True(t, a.ContainsAll())
	assert.False(t, a.ContainsAll(1))

	a.Insert(1)
	assert.True(t, a.ContainsAll(1))
	assert.False(t, a.ContainsAll(1, 2))

	a.Insert(2)
	assert.True(t, a.ContainsAll(1, 2))
	assert.False(t, a.ContainsAll(1, 2, 3))

	a.Insert(3)
	assert.True(t, a.ContainsAll(1, 2))
	assert.True(t, a.ContainsAll(1, 2, 3))
}

func TestContainsAny(t *testing.T) {
	a := make(Set[int])
	assert.False(t, a.ContainsAny())
	assert.False(t, a.ContainsAny(1))

	a.Insert(1)
	assert.True(t, a.ContainsAny(1))
	assert.True(t, a.ContainsAny(1, 2))
	assert.False(t, a.ContainsAny(2, 3))

	a.Insert(2)
	assert.True(t, a.ContainsAny(1))
	assert.True(t, a.ContainsAny(2))
	assert.False(t, a.ContainsAny(3))
	assert.True(t, a.ContainsAny(1, 2))

	a.Insert(3)
	assert.True(t, a.ContainsAny(3))
	assert.True(t, a.ContainsAny(1, 2, 3))
}

func TestInsertAll(t *testing.T) {
	a := New[int](1)
	a.InsertAll()
	assert.Equal(t, New[int](1), a, "InsertAll should do nothing with no elements")

	a.InsertAll(2, 3)
	assert.Equal(t, New[int](1, 2, 3), a, "InsertAll did not insert all elements")
}

func TestDeleteAll(t *testing.T) {
	a := New[int](1, 2, 3)
	a.DeleteAll()
	assert.Equal(t, New[int](1, 2, 3), a, "DeleteAll should do nothing with no elements")

	a.DeleteAll(1, 3)
	assert.Equal(t, New[int](2), a, "DeleteAll did not delete all of the expected elements")
}

func TestUnion(t *testing.T) {
	set1 := make(Set[int])
	set2 := make(Set[int])

	assert.Equal(t, New[int](), set1.Union(set2), "Unexpected union of empty sets")

	set1.Insert(1)
	assert.Equal(t, set1, set1.Union(set2), "Union where only LHS has elements")
	assert.Equal(t, set1, set2.Union(set1), "Union where only RHS has elements")

	set2.Insert(2)
	assert.Equal(t, New[int](1, 2), set1.Union(set2), "Union should contain elements from both sets")
	assert.Equal(t, New[int](1), set1, "LHS should not be modified by union")
	assert.Equal(t, New[int](2), set2, "RHS should not be modified by union")

	set2.Insert(1)
	assert.Equal(t, New[int](1, 2), set1.Union(set2), "Union should contain elements from both sets")
}

func TestCopy(t *testing.T) {
	set1 := make(Set[int])

	assert.Equal(t, New[int](), set1.Copy(), "Unexpected copy of empty set")

	set2 := set1.Copy()
	set1.Insert(1)
	assert.NotEqual(t, set1, set2, "Copy should not be affected by modifications to original set")
	assert.Equal(t, New[int](1), set1.Copy(), "Unexpected copy of set with elements")
}

func TestString(t *testing.T) {
	s := New[int](1, 2, 3)
	elements := []string{fmt.Sprint(1), fmt.Sprint(2), fmt.Sprint(3)}
	sort.Strings(elements)
	expected := fmt.Sprint(elements)
	str := s.String()
	assert.Equal(t, expected, str)
}

func TestStringEmpty(t *testing.T) {
	s := New[int]()
	assert.Equal(t, "[]", s.String())
}

func TestToSortedList(t *testing.T) {
	s := New[int](3, 1, 2)
	sorted := ToSortedList(s)
	assert.Equal(t, []int{1, 2, 3}, sorted)
}

func TestToSortedEmpty(t *testing.T) {
	s := New[string]()
	sorted := ToSortedList(s)
	assert.Nil(t, sorted)
}

func BenchmarkSet_String(b *testing.B) {
	tests := []struct {
		giveLen int
	}{
		{10},
		{50},
		{100},
	}
	for _, tt := range tests {
		b.Run(strconv.Itoa(tt.giveLen), func(b *testing.B) {
			s := newRandomSet(tt.giveLen)
			for i := 0; i < b.N; i++ {
				_ = s.String()
			}
		})
	}
}

func BenchmarkSet_Copy(b *testing.B) {
	tests := []struct {
		size int
	}{
		{2},
		{10},
		{100},
	}
	for _, tt := range tests {
		s := newRandomSet(tt.size)
		b.Run(strconv.Itoa(tt.size), func(b *testing.B) {
			for range b.N {
				s.Copy()
			}
		})
	}
}

func BenchmarkSet_Union(b *testing.B) {
	tests := []struct {
		s1Size int
		s2Size int
	}{
		{
			s1Size: 1,
			s2Size: 1,
		},
		{
			s1Size: 1,
			s2Size: 10,
		},
		{
			s1Size: 10,
			s2Size: 10,
		},
		{
			s1Size: 10,
			s2Size: 1000,
		},
		{
			s1Size: 500,
			s2Size: 1000,
		},
		{
			s1Size: 1001,
			s2Size: 1000,
		},
	}
	for _, tt := range tests {
		s1 := newRandomSet(tt.s1Size)
		s2 := newRandomSet(tt.s2Size)
		b.Run(fmt.Sprintf("s1-size=%v s2-size=%v", tt.s1Size, tt.s2Size), func(b *testing.B) {
			b.Run("s1 union s2", func(b *testing.B) {
				for range b.N {
					s1.Union(s2)
				}
			})
			b.Run("s2 union s1", func(b *testing.B) {
				for range b.N {
					s2.Union(s1)
				}
			})
		})
	}
}

func newRandomSet(size int) Set[int] {
	s := New[int]()
	for range size {
		s.Insert(rand.Int())
	}
	return s
}
