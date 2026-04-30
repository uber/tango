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

package targethasher

import (
	"context"
	"io"
	"regexp"
	"testing"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	set "github.com/deckarep/golang-set/v2"
	"github.com/golang/mock/gomock"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uber/tango/core/bazel"
)

func StringPtr(s string) *string {
	return &s
}

func TestEmptyResult(t *testing.T) {
	r := EmptyResult()
	assert.Empty(t, r.TargetNames, nil)
	assert.Empty(t, r.Targets)
	assert.Empty(t, r.Warnings)
}

func TestFromProtoWithCyclicDependenciesNoRoot(t *testing.T) {
	qr := buildpb.QueryResult{Target: []*buildpb.Target{
		{
			Type: buildpb.Target_RULE.Enum(),
			Rule: &buildpb.Rule{
				Name:      StringPtr("//:a"),
				RuleInput: []string{"//:b"},
			},
		},
		{
			Type: buildpb.Target_RULE.Enum(),
			Rule: &buildpb.Rule{
				Name:      StringPtr("//:b"),
				RuleInput: []string{"//:a"},
			},
		},
	}}

	result, err := FromProto(context.Background(), &qr, t.TempDir(), HashConfig{})
	require.NoError(t, err)

	assert.Len(t, result.TargetNames, 2)
	assert.Len(t, result.Targets, 2)

	for _, target := range result.Targets {
		assert.True(t, target.Root)
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	cancelFunc()

	// verify fromProto honors context cancellation
	qr := &buildpb.QueryResult{
		Target: []*buildpb.Target{&buildpb.Target{}},
	}
	result, err := fromProto(ctx, qr, nil, "", set.NewSet[string](), nil, false)
	assert.Equal(t, EmptyResult(), result)
	assert.ErrorIs(t, err, context.Canceled)

	// verify fromProto honors context cancellation
	bytes, err := HashRecursively(ctx, HashParam{})
	assert.Empty(t, bytes)
	assert.ErrorIs(t, err, context.Canceled)

	// verify fromProto honors context cancellation
	strs, err := ToposortRecursively(ctx, nil, "", nil, nil)
	assert.Empty(t, strs)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestFromProtoSimpleRule(t *testing.T) {
	qr := &buildpb.QueryResult{
		Target: []*buildpb.Target{
			{
				Type: buildpb.Target_RULE.Enum(),
				Rule: &buildpb.Rule{
					Name:      StringPtr("//pkg:lib"),
					RuleClass: StringPtr("go_library"),
				},
			},
		},
	}

	result, err := fromProto(context.Background(), qr, &noOpHasher{}, "", set.NewSet[string](), nil, false)
	require.NoError(t, err)

	assert.Len(t, result.Targets, 1)
	assert.Len(t, result.TargetNames, 1)
	assert.Contains(t, result.Targets, "//pkg:lib")
	assert.True(t, result.Targets["//pkg:lib"].Root)
	assert.Equal(t, "go_library", result.Targets["//pkg:lib"].RuleType)
}

func TestFromProtoWithDependencies(t *testing.T) {
	qr := &buildpb.QueryResult{
		Target: []*buildpb.Target{
			{
				Type: buildpb.Target_RULE.Enum(),
				Rule: &buildpb.Rule{
					Name:      StringPtr("//pkg:app"),
					RuleClass: StringPtr("go_binary"),
					RuleInput: []string{"//pkg:lib"},
				},
			},
			{
				Type: buildpb.Target_RULE.Enum(),
				Rule: &buildpb.Rule{
					Name:      StringPtr("//pkg:lib"),
					RuleClass: StringPtr("go_library"),
				},
			},
		},
	}

	result, err := fromProto(context.Background(), qr, &noOpHasher{}, "", set.NewSet[string](), nil, false)
	require.NoError(t, err)

	assert.Len(t, result.Targets, 2)
	assert.Len(t, result.TargetNames, 2)

	// app depends on lib, so lib should come first in topological order
	assert.Equal(t, "//pkg:lib", result.TargetNames[0])
	assert.Equal(t, "//pkg:app", result.TargetNames[1])

	// only app should be a root (lib is a dependency)
	assert.True(t, result.Targets["//pkg:app"].Root)
	assert.False(t, result.Targets["//pkg:lib"].Root)
}

func TestFromProtoWithExcludedRegex(t *testing.T) {
	qr := &buildpb.QueryResult{
		Target: []*buildpb.Target{
			{
				Type: buildpb.Target_RULE.Enum(),
				Rule: &buildpb.Rule{
					Name:      StringPtr("//pkg:app"),
					RuleClass: StringPtr("go_binary"),
				},
			},
			{
				Type: buildpb.Target_RULE.Enum(),
				Rule: &buildpb.Rule{
					Name:      StringPtr("//vendor:lib"),
					RuleClass: StringPtr("go_library"),
				},
			},
		},
	}

	// Exclude targets matching "//vendor:.*"
	excludedRegex := []*regexp.Regexp{regexp.MustCompile("//vendor:.*")}

	result, err := fromProto(context.Background(), qr, &noOpHasher{}, "", set.NewSet[string](), excludedRegex, false)
	require.NoError(t, err)

	assert.Len(t, result.Targets, 2)
	// Excluded target should have empty hash
	assert.Empty(t, result.Targets["//vendor:lib"].Hash)
}

func TestFromProtoWithGeneratedFile(t *testing.T) {
	qr := &buildpb.QueryResult{
		Target: []*buildpb.Target{
			{
				Type: buildpb.Target_RULE.Enum(),
				Rule: &buildpb.Rule{
					Name:      StringPtr("//pkg:generator"),
					RuleClass: StringPtr("genrule"),
				},
			},
			{
				Type: buildpb.Target_GENERATED_FILE.Enum(),
				GeneratedFile: &buildpb.GeneratedFile{
					Name:           StringPtr("//pkg:generated.go"),
					GeneratingRule: StringPtr("//pkg:generator"),
				},
			},
		},
	}

	result, err := fromProto(context.Background(), qr, &noOpHasher{}, "", set.NewSet[string](), nil, false)
	require.NoError(t, err)

	assert.Len(t, result.Targets, 2)
	assert.Contains(t, result.Targets, "//pkg:generator")
	assert.Contains(t, result.Targets, "//pkg:generated.go")
	assert.Equal(t, GeneratedFileType, result.Targets["//pkg:generated.go"].RuleType)
}

func Test_RemoveAttrs(t *testing.T) {
	regularTarget := &buildpb.Target{
		Type: buildpb.Target_RULE.Enum(),
		Rule: &buildpb.Rule{
			Name: StringPtr("//pkg:go_default_library"),
			Attribute: []*buildpb.Attribute{
				&buildpb.Attribute{
					Name:        StringPtr("url"),
					StringValue: StringPtr("some_url"),
				},
				&buildpb.Attribute{
					Name:            StringPtr("urls"),
					StringListValue: []string{"url1", "url2"},
				},
				&buildpb.Attribute{
					Name:        StringPtr("to_keep"),
					StringValue: StringPtr("target1"),
				},
			},
		},
	}
	target, err := toTarget(regularTarget)
	assert.NoError(t, err)
	assert.False(t, target.External)
	assert.Equal(t, regularTarget.GetRule().GetAttribute(), target.Attributes)
	assert.Equal(t, []byte{0x69, 0x8b, 0x5, 0x75, 0x55, 0x80, 0x66, 0x5d, 0x7e, 0xbc, 0x75, 0x4, 0x8e, 0x62, 0x48, 0xb0, 0x82, 0x9c, 0x87, 0x82}, target.HashWithoutDeps)

	externalTarget := &buildpb.Target{
		Type: buildpb.Target_RULE.Enum(),
		Rule: &buildpb.Rule{
			Name: StringPtr("//external:some_rule"),
			Attribute: []*buildpb.Attribute{
				&buildpb.Attribute{
					Name:        StringPtr("url"),
					StringValue: StringPtr("some_url"),
				},
				&buildpb.Attribute{
					Name:            StringPtr("urls"),
					StringListValue: []string{"url1", "url2"},
				},
				&buildpb.Attribute{
					Name:        StringPtr("to_keep"),
					StringValue: StringPtr("external"),
				},
			},
		},
	}
	external, err := toTarget(externalTarget)
	assert.NoError(t, err)
	assert.True(t, external.External)
	assert.Equal(t, []byte{0xbb, 0xee, 0x72, 0xbb, 0xda, 0x44, 0xa0, 0xb5, 0x27, 0x9f, 0x9c, 0xde, 0xda, 0xb3, 0xc9, 0x46, 0xbe, 0x7e, 0x14, 0x92}, external.HashWithoutDeps)

	// add sha256 attribute, hash should change
	externalTarget.Rule.Attribute = append(externalTarget.Rule.Attribute, &buildpb.Attribute{
		Name:        StringPtr("sha256"),
		StringValue: StringPtr("some_hash"),
	})
	external, err = toTarget(externalTarget)
	assert.NoError(t, err)
	assert.True(t, external.External)
	assert.Equal(t, []byte{0x7c, 0xde, 0x91, 0xc2, 0x94, 0x1a, 0x22, 0xf3, 0xb2, 0x18, 0x7c, 0x21, 0xbf, 0x32, 0x17, 0xc0, 0xa3, 0xf0, 0xc, 0x77}, external.HashWithoutDeps)
}

func validateResultIsStable(t *testing.T, baseResult, result Result) {
	t.Helper()
	require.ElementsMatch(t, baseResult.TargetNames, result.TargetNames)
	for _, targetName := range baseResult.TargetNames {
		base, ok := baseResult.Targets[targetName]
		require.True(t, ok)
		res, ok := result.Targets[targetName]
		require.True(t, ok)
		assert.Equal(t, base.Hash, res.Hash)
	}
}

func assertEqualTargetHash(t *testing.T, expected, actual Target) {
	opt := cmpopts.IgnoreUnexported(Target{})
	// too many nested attributes to compare
	ignore := cmpopts.IgnoreFields(Target{}, "Attributes", "SourceFile", "Rule")
	assert.True(t, cmp.Equal(expected, actual, opt, ignore), cmp.Diff(expected, actual, opt))
}

func Test_fromProto(t *testing.T) {
	ctrl := gomock.NewController(t)

	mockHasher := NewMockSourceHasher(ctrl)
	mockHasher.EXPECT().
		HashSourceFile(gomock.Any()).
		DoAndReturn(func(s *buildpb.SourceFile) ([]byte, error) {
			h := newHash()
			io.WriteString(h, s.GetName())
			return h.Sum(nil), nil
		}).AnyTimes()

	// bazel query "deps(//...:all-targets)" --enable_bzlmod --order_output=no --proto:locations --noproto:default_values --output=proto > core/targethasher/testdata/test.proto.bin
	q, err := bazel.FromFile("testdata/test.proto.bin")
	require.NoError(t, err)

	a, err := fromProto(context.Background(), q, mockHasher, "", set.NewSet[string](), nil, true)
	require.NoError(t, err)

	assert.Empty(t, a.Warnings)
	assert.Len(t, a.Targets, len(a.TargetNames))
	t.Log(a.TargetNames)
	assert.Equal(t, len(a.TargetNames), 3640)
}
