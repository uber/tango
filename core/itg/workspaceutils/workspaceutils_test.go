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

package workspaceutils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelFilePathToTargetName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		relFilePath string
		want        string
	}{
		{
			name:        "simple package",
			relFilePath: "pkg/file.go",
			want:        "//pkg:file.go",
		},
		{
			name:        "two levels deep",
			relFilePath: "pkg/sub/file.go",
			want:        "//pkg/sub:file.go",
		},
		{
			name:        "deeply nested",
			relFilePath: "a/b/c/d/file.go",
			want:        "//a/b/c/d:file.go",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, RelFilePathToTargetName(tt.relFilePath))
		})
	}
}

func TestRelFilePathsToTargetNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		relFilePaths []string
		want         []string
	}{
		{
			name:         "empty slice",
			relFilePaths: []string{},
			want:         []string{},
		},
		{
			name:         "single file",
			relFilePaths: []string{"pkg/file.go"},
			want:         []string{"//pkg:file.go"},
		},
		{
			name:         "multiple files",
			relFilePaths: []string{"a/b/c.go", "d/e.go"},
			want:         []string{"//a/b:c.go", "//d:e.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, RelFilePathsToTargetNames(tt.relFilePaths))
		})
	}
}

func TestPkgNameToTargetName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		pkg  string
		want string
	}{
		{
			name: "dot is root package",
			pkg:  ".",
			want: "//:*",
		},
		{
			name: "empty string is root package",
			pkg:  "",
			want: "//:*",
		},
		{
			name: "simple package",
			pkg:  "pkg",
			want: "//pkg:*",
		},
		{
			name: "nested package",
			pkg:  "a/b/c",
			want: "//a/b/c:*",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, PkgNameToTargetName(tt.pkg))
		})
	}
}

func TestGetContainingPackage(t *testing.T) {
	t.Parallel()

	mkBuild := func(t *testing.T, dir, name string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o644))
	}

	t.Run("file in package with BUILD.bazel", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		pkgDir := filepath.Join(root, "pkg", "sub")
		require.NoError(t, os.MkdirAll(pkgDir, 0o755))
		mkBuild(t, pkgDir, "BUILD.bazel")

		got, err := GetContainingPackage(root, "pkg/sub/file.go")
		require.NoError(t, err)
		assert.Equal(t, "pkg/sub", got)
	})

	t.Run("file in package with BUILD", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		pkgDir := filepath.Join(root, "pkg")
		require.NoError(t, os.MkdirAll(pkgDir, 0o755))
		mkBuild(t, pkgDir, "BUILD")

		got, err := GetContainingPackage(root, "pkg/file.go")
		require.NoError(t, err)
		assert.Equal(t, "pkg", got)
	})

	t.Run("file in subdirectory without BUILD climbs to parent", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		pkgDir := filepath.Join(root, "pkg")
		subDir := filepath.Join(pkgDir, "internal")
		require.NoError(t, os.MkdirAll(subDir, 0o755))
		mkBuild(t, pkgDir, "BUILD.bazel")

		got, err := GetContainingPackage(root, "pkg/internal/file.go")
		require.NoError(t, err)
		assert.Equal(t, "pkg", got)
	})

	t.Run("BUILD.bazel at workspace root returns empty string", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		pkgDir := filepath.Join(root, "pkg")
		require.NoError(t, os.MkdirAll(pkgDir, 0o755))
		mkBuild(t, root, "BUILD.bazel")

		got, err := GetContainingPackage(root, "pkg/file.go")
		require.NoError(t, err)
		assert.Equal(t, "", got)
	})

	t.Run("no BUILD file anywhere returns ErrParentPackageNotExist", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		pkgDir := filepath.Join(root, "pkg")
		require.NoError(t, os.MkdirAll(pkgDir, 0o755))

		_, err := GetContainingPackage(root, "pkg/file.go")
		assert.ErrorIs(t, err, ErrParentPackageNotExist)
	})

	t.Run("directory path uses parent directory", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		pkgDir := filepath.Join(root, "pkg")
		subDir := filepath.Join(pkgDir, "sub")
		require.NoError(t, os.MkdirAll(subDir, 0o755))
		mkBuild(t, pkgDir, "BUILD.bazel")

		// relPath points to a directory; GetContainingPackage finds pkg, not sub
		got, err := GetContainingPackage(root, "pkg/sub")
		require.NoError(t, err)
		assert.Equal(t, "pkg", got)
	})

	t.Run("BUILD.bazel takes precedence over BUILD", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		pkgDir := filepath.Join(root, "pkg")
		require.NoError(t, os.MkdirAll(pkgDir, 0o755))
		mkBuild(t, pkgDir, "BUILD.bazel")
		mkBuild(t, pkgDir, "BUILD")

		got, err := GetContainingPackage(root, "pkg/file.go")
		require.NoError(t, err)
		assert.Equal(t, "pkg", got)
	})
}
