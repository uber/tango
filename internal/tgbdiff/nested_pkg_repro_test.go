package tgbdiff_test

import (
	"testing"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/tgb"
)

// nestedPkgGraph builds a graph whose packages nest, so that a package string
// is a proper prefix of another package string. Bazel does this constantly
// (//src/foo and //src/foo/bar both exist).
//
// This matters because TGB sorts nodes by the (pkg, name) tuple while
// fastdiff's phase 0 merge-join compares whole label strings. The two orders
// disagree exactly here: '/' (0x2F) sorts below ':' (0x3A), so
//
//	tuple order:  ("//a", "t") < ("//a/b", "t")
//	string order: "//a/b:t"    < "//a:t"
//
// are opposite.
func nestedPkgGraph(drop string) *tgb.Graph {
	pkgs := []string{"//a", "//a/b", "//a/b/c", "//a/z"}
	rtMap := map[int32]string{1: "go_library", 2: "source file"}

	var targets []entity.OptimizedTarget
	idMap := map[int32]string{}
	var id int32
	add := func(label string) {
		if label == drop {
			return
		}
		id++
		idMap[id] = label
		targets = append(targets, entity.OptimizedTarget{
			ID:       id,
			RuleType: 1,
			Hash:     labelHash(label),
		})
	}
	for _, p := range pkgs {
		add(p + ":lib")
		add(p + ":test")
	}

	return &tgb.Graph{
		Targets: targets,
		Metadata: entity.Metadata{
			TargetIDMapping:             idMap,
			RuleTypeMapping:             rtMap,
			TagMapping:                  map[int32]string{},
			AttributeNameMapping:        map[int32]string{},
			AttributeStringValueMapping: map[int32]string{},
		},
	}
}

// TestNestedPackageOrdering checks fastdiff against the refdiff oracle when
// package names nest. A failure here means phase 0's merge-join is pairing the
// wrong labels.
func TestNestedPackageOrdering(t *testing.T) {
	// Each case removes one target, which forces the phase 0 merge-join to
	// resolve an inequality across a package-nesting boundary. The synthetic
	// corpus in fastdiff_test.go uses flat //src/pkgNNNN packages and never
	// exercises this.
	for _, drop := range []string{
		"//a:lib", "//a:test",
		"//a/b:lib", "//a/b:test",
		"//a/b/c:lib", "//a/b/c:test",
		"//a/z:lib", "//a/z:test",
	} {
		before := nestedPkgGraph("")
		after := nestedPkgGraph(drop)
		for _, blockSize := range []int{2, 4, 64} {
			diffEqual(t, "nested-pkg-drop"+drop, before, after, blockSize)
		}
		// And the reverse direction: the same shape as an insertion.
		diffEqual(t, "nested-pkg-add"+drop, after, before, 2)
	}
}
