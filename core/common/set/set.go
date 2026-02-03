// Package set implements a generic set of comparable objects.
// Objects in a set are unordered.
package set

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
)

// Set is a set of Ts.
//
// Set is not safe for concurrent use. Guard set reads and writes with a lock
// if you need to use it from multiple goroutines.
//
// nil is a valid empty set for reads. Writes to a nil set will panic.
type Set[T comparable] map[T]struct{}

// Note: The set is backed by a struct{} instead of a bool.
// struct{} takes zero space at runtime.

// New builds a set populated with the provided items.
func New[T comparable](items ...T) Set[T] {
	s := NewCap[T](len(items))
	for _, i := range items {
		s[i] = struct{}{}
	}
	return s
}

// NewCap builds an empty set with pre-allocated capacity for at least count
// items.
func NewCap[T comparable](count int) Set[T] {
	return make(Set[T], count)
}

// Equal reports whether other is equal to this set.
func (s Set[T]) Equal(other Set[T]) bool {
	if len(s) != len(other) {
		return false
	}

	for item := range s {
		if !other.Contains(item) {
			return false
		}
	}

	return true
}

// Contains reports whether the set contains the provided item.
func (s Set[T]) Contains(item T) bool {
	_, ok := s[item]
	return ok
}

// ContainsAll reports whether the set contains all of the provided items.
func (s Set[T]) ContainsAll(items ...T) bool {
	for _, item := range items {
		if !s.Contains(item) {
			return false
		}
	}
	return true
}

// ContainsAny reports whether the set contains any of the provided items.
func (s Set[T]) ContainsAny(items ...T) bool {
	for _, item := range items {
		if s.Contains(item) {
			return true
		}
	}
	return false
}

// Insert adds an item to the set.
func (s Set[T]) Insert(item T) {
	s[item] = struct{}{}
}

// InsertUnique adds an item to the set but only if it's not already present.
// InsertUnique reports whether the item was successfully added.
func (s Set[T]) InsertUnique(item T) (ok bool) {
	if s.Contains(item) {
		return false
	}
	s.Insert(item)
	return true
}

// InsertAll adds the provided items to the set.
func (s Set[T]) InsertAll(items ...T) {
	for _, item := range items {
		s.Insert(item)
	}
}

// Delete removes an item from the set.
// Delete is a no-op if the item is not in the set.
func (s Set[T]) Delete(item T) {
	delete(s, item)
}

// CheckedDelete removes an item from the set, reporting whether it was
// present.
func (s Set[T]) CheckedDelete(item T) (ok bool) {
	ok = s.Contains(item)
	delete(s, item)
	return ok
}

// DeleteAll removes the provided items from the set.
// DeleteAll is a no-op for any items not present in the set.
func (s Set[T]) DeleteAll(items ...T) {
	for _, item := range items {
		s.Delete(item)
	}
}

// Clear clears all elements from the set.
// This is useful for reusing a set's allocated memory. After a call to Clear,
// the set will be empty, but its underlying capacity will be preserved,
// avoiding the need for a new memory allocation on subsequent insertions.
// Clear panics if the set is nil.
func (s Set[T]) Clear() {
	clear(s)
}

// Difference builds a new set holding elements present in this set but not in
// other.
func (s Set[T]) Difference(other Set[T]) Set[T] {
	output := NewCap[T](0)
	for item := range s {
		if !other.Contains(item) {
			output.Insert(item)
		}
	}
	return output
}

// SymDifference builds a new set holding elements present in either this set
// or other, but not in both.
func (s Set[T]) SymDifference(other Set[T]) Set[T] {
	output := NewCap[T](0)
	for item := range s {
		if !other.Contains(item) {
			output.Insert(item)
		}
	}
	for item := range other {
		if !s.Contains(item) {
			output.Insert(item)
		}
	}
	return output
}

// Intersection builds a new set holding elements that are present in both
// sets, but not in just one.
func (s Set[T]) Intersection(other Set[T]) Set[T] {
	big, small := compareSetSize(s, other)
	output := NewCap[T](len(small))
	for item := range small {
		if big.Contains(item) {
			output.Insert(item)
		}
	}
	return output
}

// Union builds a new set holding elements present in either this set or other.
func (s Set[T]) Union(other Set[T]) Set[T] {
	big, small := compareSetSize(s, other)
	// For backward-compatibilty; previous version always returns
	// a non-nil set even if s and other are both nil, but Clone will return nil.
	if len(big) == 0 {
		return NewCap[T](0)
	}
	output := maps.Clone(big)
	maps.Copy(output, small)
	return output
}

// Copy returns a copy of this set.
func (s Set[T]) Copy() Set[T] {
	return s.Union(nil)
}

// UnsortedList builds a slice of all elements of this slice in an unspecified
// order.
//
// UnsortedList does not allocate a slice if the set is empty, returning nil in
// that case.
func (s Set[T]) UnsortedList() []T {
	if len(s) == 0 {
		return nil
	}

	output := make([]T, 0, len(s))
	for item := range s {
		output = append(output, item)
	}
	return output
}

func (s Set[T]) String() string {
	list := make([]string, 0, len(s))
	for item := range s {
		list = append(list, fmt.Sprint(item))
	}
	slices.Sort(list)
	return fmt.Sprint(list)
}

// ToSortedList returns a sorted slice containing all elements of the set. This
// is a convenience function for sets with elements that can be compared
// using `<`.
//
// ToSortedList does not allocate a slice if the set is empty, returning nil in
// that case.
func ToSortedList[T cmp.Ordered](s Set[T]) []T {
	unsorted := s.UnsortedList()
	slices.Sort(unsorted)
	return unsorted
}

func compareSetSize[T comparable](s1, s2 Set[T]) (big, small Set[T]) {
	if len(s1) > len(s2) {
		return s1, s2
	}
	return s2, s1
}
