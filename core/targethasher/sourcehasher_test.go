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
	"errors"
	"os"
	"path/filepath"
	"testing"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TODO: Add tests to check for bazel query for proto.bin output.
func TestNewSourceHasher_buildsMaps(t *testing.T) {
	h := NewSourceHasher(Params{
		WorkspaceRoot: "/ws",
		HashConfig: HashConfig{
			KnownSourceHashes: map[string][]byte{"a/b.txt": []byte("abc")},
		},
	})
	dh, ok := h.(*diskHashHelper)
	assert.True(t, ok, "expected *diskHashHelper, got %T", h)
	assert.Equal(t, "/ws", dh.workspaceroot, "workspaceroot mismatch")
	assert.Equal(t, "abc", string(dh.knownFileHashes["a/b.txt"]), "knownFileHashes mismatch")
}

func TestNoOpHasher_returnsNil(t *testing.T) {
	h := &noOpHasher{}
	sf := &buildpb.SourceFile{Name: strPtr("//:dummy")}
	got, err := h.HashSourceFile(context.Background(), sf)
	assert.NoError(t, err, "unexpected error: %v", err)
	assert.Nil(t, got, "expected nil hash, got %v", got)
}

func TestDiskHashHelper_KnownFileHashUsed(t *testing.T) {
	tmp := t.TempDir()
	wsRoot := tmp
	rel := filepath.Join("pkg", "file.txt")
	abs := filepath.Join(wsRoot, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		require.NoError(t, err, "mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte("contents"), 0o644); err != nil {
		require.NoError(t, err, "write: %v", err)
	}

	known := []byte("KNOWN")
	h := &diskHashHelper{
		workspaceroot:   wsRoot,
		knownFileHashes: map[string][]byte{filepath.ToSlash(rel): known},
	}
	sf := &buildpb.SourceFile{
		Name:            strPtr("//" + filepath.ToSlash(filepath.Dir(rel)) + ":" + filepath.Base(rel)),
		Location:        strPtr(abs + ":1:1"),
		VisibilityLabel: []string{"//visibility:private"},
	}
	got, err := h.HashSourceFile(context.Background(), sf)
	assert.NoError(t, err, "unexpected err: %v", err)
	assert.Equal(t, string(known), string(got), "expected known hash %q, got %q", known, got)
}

func TestDiskHashHelper_NonDefaultVisibilityForcesDiskHash(t *testing.T) {
	tmp := t.TempDir()
	wsRoot := tmp
	rel := filepath.Join("pkg", "file.txt")
	abs := filepath.Join(wsRoot, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		require.NoError(t, err, "mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte("X"), 0o644); err != nil {
		require.NoError(t, err, "write: %v", err)
	}

	h := &diskHashHelper{
		workspaceroot:   wsRoot,
		knownFileHashes: map[string][]byte{filepath.ToSlash(rel): []byte("KNOWN")},
	}
	sf := &buildpb.SourceFile{
		Name:            strPtr("//" + filepath.ToSlash(filepath.Dir(rel)) + ":" + filepath.Base(rel)),
		Location:        strPtr(abs + ":1:1"),
		VisibilityLabel: []string{"//visibility:public"}, // non-default
	}
	got, err := h.HashSourceFile(context.Background(), sf)
	assert.NoError(t, err, "unexpected err: %v", err)
	assert.NotEqual(t, string(got), "KNOWN", "expected disk hash, but got known hash")
	assert.NotEqual(t, []byte{}, got, "expected non-empty disk hash")
}

func TestDiskHashHelper_HashesFileFromDisk(t *testing.T) {
	tmp := t.TempDir()
	wsRoot := tmp
	rel := filepath.Join("pkg", "file.txt")
	abs := filepath.Join(wsRoot, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		require.NoError(t, err, "mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte("hello world"), 0o644); err != nil {
		require.NoError(t, err, "write: %v", err)
	}

	h := &diskHashHelper{
		workspaceroot:   wsRoot,
		knownFileHashes: map[string][]byte{},
	}
	sf := &buildpb.SourceFile{
		Name:            strPtr("//" + filepath.ToSlash(filepath.Dir(rel)) + ":" + filepath.Base(rel)),
		Location:        strPtr(abs),
		VisibilityLabel: []string{"//visibility:private"},
	}
	got, err := h.HashSourceFile(context.Background(), sf)
	assert.NoError(t, err, "unexpected err: %v", err)
	assert.NotEmpty(t, got, "expected non-empty hash from disk")
}

func TestDiskHashHelper_HashesDirectory(t *testing.T) {
	tmp := t.TempDir()
	wsRoot := tmp
	dirRel := "mydir"
	dirAbs := filepath.Join(wsRoot, dirRel)
	if err := os.MkdirAll(dirAbs, 0o755); err != nil {
		require.NoError(t, err, "mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirAbs, "a.txt"), []byte("aaa"), 0o644); err != nil {
		require.NoError(t, err, "write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirAbs, "b.txt"), []byte("bbb"), 0o644); err != nil {
		require.NoError(t, err, "write b.txt: %v", err)
	}

	h := &diskHashHelper{
		workspaceroot:   wsRoot,
		knownFileHashes: map[string][]byte{},
	}
	sf := &buildpb.SourceFile{
		Name:            strPtr("//:" + dirRel),
		Location:        strPtr(dirAbs),
		VisibilityLabel: []string{"//visibility:private"},
	}
	got, err := h.HashSourceFile(context.Background(), sf)
	assert.NoError(t, err, "unexpected err: %v", err)
	assert.NotEmpty(t, got, "expected non-empty hash for directory")
}

func TestHashDir_RenameChangesHash(t *testing.T) {
	before := t.TempDir()
	after := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(before, "before.txt"), []byte("same contents"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(after, "after.txt"), []byte("same contents"), 0o644))

	assert.NotEqual(t, hashDirectoryForTest(t, before), hashDirectoryForTest(t, after))
}

func TestHashDir_HashesSymlinkEntriesAndTargets(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "first.txt"), []byte("same contents"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "second.txt"), []byte("same contents"), 0o644))

	withoutSymlink := hashDirectoryForTest(t, root)
	link := filepath.Join(root, "link")
	if err := os.Symlink("first.txt", link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	firstTarget := hashDirectoryForTest(t, root)
	assert.NotEqual(t, withoutSymlink, firstTarget)

	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink("second.txt", link))
	secondTarget := hashDirectoryForTest(t, root)
	assert.NotEqual(t, firstTarget, secondTarget)

	require.NoError(t, os.Rename(link, filepath.Join(root, "renamed-link")))
	assert.NotEqual(t, secondTarget, hashDirectoryForTest(t, root))
}

func TestHashDir_NormalizesAbsoluteSymlinkTargets(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	writeDirectoryTestFile(t, left, "target.txt", "same contents")
	writeDirectoryTestFile(t, right, "target.txt", "same contents")

	if err := os.Symlink(filepath.Join(left, "target.txt"), filepath.Join(left, "link")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	require.NoError(t, os.Symlink(filepath.Join(right, "target.txt"), filepath.Join(right, "link")))

	assert.Equal(t, hashDirectoryForTest(t, left), hashDirectoryForTest(t, right))
}

func TestHashDir_IsDeterministicAcrossCreationOrder(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()

	for _, relativePath := range []string{"z.txt", filepath.Join("nested", "b.txt"), filepath.Join("nested", "a.txt")} {
		writeDirectoryTestFile(t, left, relativePath, relativePath)
	}
	for _, relativePath := range []string{filepath.Join("nested", "a.txt"), filepath.Join("nested", "b.txt"), "z.txt"} {
		writeDirectoryTestFile(t, right, relativePath, relativePath)
	}

	leftHash := hashDirectoryForTest(t, left)
	assert.Equal(t, leftHash, hashDirectoryForTest(t, left))
	assert.Equal(t, leftHash, hashDirectoryForTest(t, right))
}

func TestHashDir_DistinguishesDirectoryStructures(t *testing.T) {
	t.Run("regular file from nested file", func(t *testing.T) {
		fileTree := t.TempDir()
		nestedTree := t.TempDir()
		writeDirectoryTestFile(t, fileTree, "entry", "same contents")
		writeDirectoryTestFile(t, nestedTree, filepath.Join("entry", "child"), "same contents")

		assert.NotEqual(t, hashDirectoryForTest(t, fileTree), hashDirectoryForTest(t, nestedTree))
	})

	t.Run("empty directory from empty tree", func(t *testing.T) {
		emptyTree := t.TempDir()
		directoryTree := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(directoryTree, "empty"), 0o755))

		assert.NotEqual(t, hashDirectoryForTest(t, emptyTree), hashDirectoryForTest(t, directoryTree))
	})
}

func TestHashDir_OmitsFileMode(t *testing.T) {
	regularTree := t.TempDir()
	executableTree := t.TempDir()
	writeDirectoryTestFile(t, regularTree, "tool", "same contents")
	writeDirectoryTestFile(t, executableTree, "tool", "same contents")
	require.NoError(t, os.Chmod(filepath.Join(regularTree, "tool"), 0o644))
	require.NoError(t, os.Chmod(filepath.Join(executableTree, "tool"), 0o755))

	assert.Equal(t, hashDirectoryForTest(t, regularTree), hashDirectoryForTest(t, executableTree))
}

func hashDirectoryForTest(t *testing.T, root string) []byte {
	t.Helper()
	h, err := hashDir(context.Background(), root)
	require.NoError(t, err)
	return h.Sum(nil)
}

func writeDirectoryTestFile(t *testing.T, root, relativePath, contents string) {
	t.Helper()
	path := filepath.Join(root, relativePath)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
}

func TestDiskHashHelper_MissingFileProducesDeterministicHash(t *testing.T) {
	h := &diskHashHelper{
		workspaceroot:   t.TempDir(),
		knownFileHashes: map[string][]byte{},
	}

	sfA := &buildpb.SourceFile{
		Name:     strPtr("//pkg:missing_a.txt"),
		Location: strPtr(filepath.Join(t.TempDir(), "does", "not", "exist_a.txt")),
	}
	sfB := &buildpb.SourceFile{
		Name:     strPtr("//pkg:missing_b.txt"),
		Location: strPtr(filepath.Join(t.TempDir(), "does", "not", "exist_b.txt")),
	}

	hashA, err := h.HashSourceFile(context.Background(), sfA)
	require.NoError(t, err)
	assert.NotEmpty(t, hashA)

	hashA2, err := h.HashSourceFile(context.Background(), sfA)
	require.NoError(t, err)
	assert.Equal(t, hashA, hashA2, "same missing file should produce the same hash")

	hashB, err := h.HashSourceFile(context.Background(), sfB)
	require.NoError(t, err)
	assert.NotEqual(t, hashA, hashB, "different missing files should produce different hashes")
}

func strPtr(s string) *string { return &s }

func valuePtr[T any](value T) *T { return &value }

func hashRuleForTest(rule *buildpb.Rule) []byte {
	h := newHash()
	HashRuleCommon(rule, h)
	return h.Sum(nil)
}

func TestHashRuleCommon_DistinguishesStructuralCollisions(t *testing.T) {
	tests := []struct {
		name  string
		left  *buildpb.Rule
		right *buildpb.Rule
	}{
		{
			name: "rule field boundaries",
			left: &buildpb.Rule{
				Name:      strPtr("a"),
				RuleClass: strPtr("bc"),
			},
			right: &buildpb.Rule{
				Name:      strPtr("ab"),
				RuleClass: strPtr("c"),
			},
		},
		{
			name: "collection element boundaries",
			left: &buildpb.Rule{
				Name:      strPtr("//pkg:target"),
				RuleClass: strPtr("test_rule"),
				Attribute: []*buildpb.Attribute{{
					Name:            strPtr("values"),
					Type:            buildpb.Attribute_STRING_LIST.Enum(),
					StringListValue: []string{"a", "bc"},
				}},
			},
			right: &buildpb.Rule{
				Name:      strPtr("//pkg:target"),
				RuleClass: strPtr("test_rule"),
				Attribute: []*buildpb.Attribute{{
					Name:            strPtr("values"),
					Type:            buildpb.Attribute_STRING_LIST.Enum(),
					StringListValue: []string{"ab", "c"},
				}},
			},
		},
		{
			name: "collection field boundaries",
			left: &buildpb.Rule{
				Name:       strPtr("//pkg:target"),
				RuleClass:  strPtr("test_rule"),
				RuleInput:  []string{"a"},
				RuleOutput: []string{"bc"},
			},
			right: &buildpb.Rule{
				Name:       strPtr("//pkg:target"),
				RuleClass:  strPtr("test_rule"),
				RuleInput:  []string{"ab"},
				RuleOutput: []string{"c"},
			},
		},
		{
			name: "scalar field and type boundaries",
			left: &buildpb.Rule{
				Name:      strPtr("//pkg:target"),
				RuleClass: strPtr("test_rule"),
				Attribute: []*buildpb.Attribute{{
					Name:     strPtr("value"),
					IntValue: valuePtr[int32](1),
				}},
			},
			right: &buildpb.Rule{
				Name:      strPtr("//pkg:target"),
				RuleClass: strPtr("test_rule"),
				Attribute: []*buildpb.Attribute{{
					Name:        strPtr("value"),
					StringValue: strPtr("1"),
				}},
			},
		},
		{
			name: "optional scalar presence",
			left: &buildpb.Rule{
				Name:      strPtr("//pkg:target"),
				RuleClass: strPtr("test_rule"),
				Attribute: []*buildpb.Attribute{{
					Name: strPtr("value"),
				}},
			},
			right: &buildpb.Rule{
				Name:      strPtr("//pkg:target"),
				RuleClass: strPtr("test_rule"),
				Attribute: []*buildpb.Attribute{{
					Name:                strPtr("value"),
					ExplicitlySpecified: valuePtr(false),
				}},
			},
		},
		{
			name: "nested message field boundaries",
			left: &buildpb.Rule{
				Name:      strPtr("//pkg:target"),
				RuleClass: strPtr("test_rule"),
				Attribute: []*buildpb.Attribute{{
					Name: strPtr("values"),
					Type: buildpb.Attribute_STRING_DICT.Enum(),
					StringDictValue: []*buildpb.StringDictEntry{{
						Key:   strPtr("a"),
						Value: strPtr("bc"),
					}},
				}},
			},
			right: &buildpb.Rule{
				Name:      strPtr("//pkg:target"),
				RuleClass: strPtr("test_rule"),
				Attribute: []*buildpb.Attribute{{
					Name: strPtr("values"),
					Type: buildpb.Attribute_STRING_DICT.Enum(),
					StringDictValue: []*buildpb.StringDictEntry{{
						Key:   strPtr("ab"),
						Value: strPtr("c"),
					}},
				}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotEqual(t, hashRuleForTest(tt.left), hashRuleForTest(tt.right))
		})
	}
}

func TestHashRuleCommon_IsDeterministicAcrossCollectionOrder(t *testing.T) {
	left := &buildpb.Rule{
		Name:           strPtr("//pkg:target"),
		RuleClass:      strPtr("test_rule"),
		RuleInput:      []string{"//pkg:b", "//pkg:a"},
		RuleOutput:     []string{"out_b", "out_a"},
		DefaultSetting: []string{"z", "a"},
		Attribute: []*buildpb.Attribute{
			{
				Name:            strPtr("tags"),
				Type:            buildpb.Attribute_STRING_LIST.Enum(),
				StringListValue: []string{"beta", "alpha"},
			},
			{
				Name: strPtr("mapping"),
				Type: buildpb.Attribute_STRING_DICT.Enum(),
				StringDictValue: []*buildpb.StringDictEntry{
					{Key: strPtr("z"), Value: strPtr("last")},
					{Key: strPtr("a"), Value: strPtr("first")},
				},
			},
			{
				Name: strPtr("filesets"),
				Type: buildpb.Attribute_FILESET_ENTRY_LIST.Enum(),
				FilesetListValue: []*buildpb.FilesetEntry{
					{
						Source:               strPtr("//pkg:z"),
						DestinationDirectory: strPtr("dest-z"),
						File:                 []string{"b", "a"},
						Exclude:              []string{"d", "c"},
					},
					{
						Source:               strPtr("//pkg:a"),
						DestinationDirectory: strPtr("dest-a"),
					},
				},
			},
		},
	}
	right := &buildpb.Rule{
		Name:           strPtr("//pkg:target"),
		RuleClass:      strPtr("test_rule"),
		RuleInput:      []string{"//pkg:a", "//pkg:b"},
		RuleOutput:     []string{"out_a", "out_b"},
		DefaultSetting: []string{"a", "z"},
		Attribute: []*buildpb.Attribute{
			{
				Name: strPtr("filesets"),
				Type: buildpb.Attribute_FILESET_ENTRY_LIST.Enum(),
				FilesetListValue: []*buildpb.FilesetEntry{
					{
						Source:               strPtr("//pkg:a"),
						DestinationDirectory: strPtr("dest-a"),
					},
					{
						Source:               strPtr("//pkg:z"),
						DestinationDirectory: strPtr("dest-z"),
						File:                 []string{"a", "b"},
						Exclude:              []string{"c", "d"},
					},
				},
			},
			{
				Name: strPtr("mapping"),
				Type: buildpb.Attribute_STRING_DICT.Enum(),
				StringDictValue: []*buildpb.StringDictEntry{
					{Key: strPtr("a"), Value: strPtr("first")},
					{Key: strPtr("z"), Value: strPtr("last")},
				},
			},
			{
				Name:            strPtr("tags"),
				Type:            buildpb.Attribute_STRING_LIST.Enum(),
				StringListValue: []string{"alpha", "beta"},
			},
		},
	}

	leftHash := hashRuleForTest(left)
	assert.Equal(t, leftHash, hashRuleForTest(left))
	assert.Equal(t, leftHash, hashRuleForTest(right))
}

func TestHashRuleCommon_IgnoresNilMessages(t *testing.T) {
	withNilMessages := &buildpb.Rule{
		Name:      strPtr("//pkg:target"),
		RuleClass: strPtr("test_rule"),
		Attribute: []*buildpb.Attribute{
			nil,
			{
				Name: strPtr("mapping"),
				Type: buildpb.Attribute_STRING_DICT.Enum(),
				StringDictValue: []*buildpb.StringDictEntry{
					nil,
					{Key: strPtr("key"), Value: strPtr("value")},
				},
			},
		},
	}
	withoutNilMessages := &buildpb.Rule{
		Name:      strPtr("//pkg:target"),
		RuleClass: strPtr("test_rule"),
		Attribute: []*buildpb.Attribute{{
			Name: strPtr("mapping"),
			Type: buildpb.Attribute_STRING_DICT.Enum(),
			StringDictValue: []*buildpb.StringDictEntry{
				{Key: strPtr("key"), Value: strPtr("value")},
			},
		}},
	}

	assert.Equal(t, hashRuleForTest(withoutNilMessages), hashRuleForTest(withNilMessages))
}

func TestDiskHashHelper_RespectsContextCancellation(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "file.txt"), []byte("x"), 0o644))

	h := &diskHashHelper{
		workspaceroot:   tmp,
		knownFileHashes: map[string][]byte{},
	}
	sf := &buildpb.SourceFile{
		Name:            strPtr("//:directory"),
		Location:        strPtr(tmp),
		VisibilityLabel: []string{"//visibility:private"},
	}

	cause := errors.New("stop hashing")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)

	_, err := h.HashSourceFile(ctx, sf)
	require.Error(t, err)
	assert.ErrorIs(t, err, cause)
}
