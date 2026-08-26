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
	result, err := fromProto(ctx, qr, nil, "", set.NewSet[string](), set.NewSet[string](), nil, false)
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

	result, err := fromProto(context.Background(), qr, &noOpHasher{}, "", set.NewSet[string](), set.NewSet[string](), nil, false)
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

	result, err := fromProto(context.Background(), qr, &noOpHasher{}, "", set.NewSet[string](), set.NewSet[string](), nil, false)
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

	result, err := fromProto(context.Background(), qr, &noOpHasher{}, "", set.NewSet[string](), set.NewSet[string](), excludedRegex, false)
	require.NoError(t, err)

	assert.Len(t, result.Targets, 2)
	// Excluded target should have empty hash
	assert.Empty(t, result.Targets["//vendor:lib"].Hash)
}

func TestFromProtoWithDoubledPackageLabel(t *testing.T) {
	// Simulates the pseudo-label Bazel query produces from a
	// workspace-relative string attribute like resource_strip_prefix: the
	// package gets doubled into the target name.
	qr := &buildpb.QueryResult{
		Target: []*buildpb.Target{
			{
				Type: buildpb.Target_RULE.Enum(),
				Rule: &buildpb.Rule{
					Name:      StringPtr("//pkg:app"),
					RuleClass: StringPtr("kt_jvm_library"),
					RuleInput: []string{"//pkg:pkg/resources"},
				},
			},
		},
	}

	result, err := FromProto(context.Background(), qr, t.TempDir(), HashConfig{})
	require.NoError(t, err)

	assert.NotContains(t, result.Targets, "//pkg:pkg/resources")
	assert.NotContains(t, result.Targets["//pkg:app"].Deps, "//pkg:pkg/resources")
	assert.NotNil(t, result.Targets["//pkg:app"].Hash)
}

func TestFromProtoAllTargetsFileHashes(t *testing.T) {
	t.Parallel()

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
	knownSourceHashes := map[string][]byte{
		".bazelrc":    {0xab, 0xcd},
		"tools/bazel": {0xef, 0x01},
		"unlisted":    {0x99},
	}

	tests := []struct {
		name            string
		allTargetsFiles []string
		want            map[string]string
	}{
		{
			name: "unset",
			want: nil,
		},
		{
			name:            "configured files present in known hashes",
			allTargetsFiles: []string{".bazelrc", "tools/bazel"},
			want:            map[string]string{".bazelrc": "abcd", "tools/bazel": "ef01"},
		},
		{
			name:            "configured file missing from known hashes is skipped",
			allTargetsFiles: []string{".bazelrc", "does/not/exist"},
			want:            map[string]string{".bazelrc": "abcd"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := FromProto(context.Background(), qr, t.TempDir(), HashConfig{
				KnownSourceHashes: knownSourceHashes,
				AllTargetsFiles:   tt.allTargetsFiles,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, result.AllTargetsFileHashes)
		})
	}
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

	result, err := fromProto(context.Background(), qr, &noOpHasher{}, "", set.NewSet[string](), set.NewSet[string](), nil, false)
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
	assert.Equal(t, []byte{0x24, 0xa2, 0x3a, 0x8, 0x9e, 0x19, 0x34, 0x6e, 0xf3, 0x62, 0x9a, 0x8f, 0xca, 0x5e, 0x54, 0xc, 0xf2, 0xdd, 0x9d, 0x8d}, target.HashWithoutDeps)

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
	assert.Equal(t, []byte{0xfd, 0xc5, 0xc7, 0x60, 0x80, 0x27, 0xc9, 0xee, 0x59, 0x2d, 0x8e, 0xb, 0x67, 0x3f, 0xae, 0xab, 0xc4, 0x8b, 0x4f, 0xe3}, external.HashWithoutDeps)

	// add sha256 attribute, hash should change
	externalTarget.Rule.Attribute = append(externalTarget.Rule.Attribute, &buildpb.Attribute{
		Name:        StringPtr("sha256"),
		StringValue: StringPtr("some_hash"),
	})
	external, err = toTarget(externalTarget)
	assert.NoError(t, err)
	assert.True(t, external.External)
	assert.Equal(t, []byte{0x6c, 0xeb, 0xec, 0x11, 0x74, 0x82, 0xae, 0x48, 0x6c, 0xff, 0x4f, 0x3c, 0xb1, 0xd2, 0xcf, 0x79, 0xf0, 0xe0, 0xee, 0xfc}, external.HashWithoutDeps)
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
	ctx := context.WithValue(context.Background(), struct{}{}, "source-hash")

	mockHasher := NewMockSourceHasher(ctrl)
	mockHasher.EXPECT().
		HashSourceFile(gomock.Eq(ctx), gomock.Any()).
		DoAndReturn(func(_ context.Context, s *buildpb.SourceFile) ([]byte, error) {
			h := newHash()
			io.WriteString(h, s.GetName())
			return h.Sum(nil), nil
		}).AnyTimes()

	// bazel query "deps(//...:all-targets)" --enable_bzlmod --order_output=no --proto:locations --noproto:default_values --output=proto > core/targethasher/testdata/test.proto.bin
	q, err := bazel.FromFile("testdata/test.proto.bin")
	require.NoError(t, err)

	a, err := fromProto(ctx, q, mockHasher, "", set.NewSet[string](), set.NewSet[string](), nil, true)
	require.NoError(t, err)

	assert.Empty(t, a.Warnings)
	assert.Len(t, a.Targets, len(a.TargetNames))
	t.Log(a.TargetNames)
	assert.Equal(t, len(a.TargetNames), 3640)
}
