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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrParentPackageNotExist is returned when parent package does not exist, i.e. there is no BUILD file in the parent directory path.
var ErrParentPackageNotExist = errors.New("parent package does not exist")

var _buildFileNames = [...]string{"BUILD.bazel", "BUILD"}

// RelFilePathToTargetName converts a relative file path to a Bazel target name.
func RelFilePathToTargetName(relFilePath string) string {
	lastSep := strings.LastIndex(relFilePath, "/")
	return "//" + relFilePath[:lastSep] + ":" + relFilePath[lastSep+1:]
}

// RelFilePathsToTargetNames converts a slice of relative file paths to a slice of Bazel target names.
func RelFilePathsToTargetNames(relFilePaths []string) []string {
	targetNames := make([]string, len(relFilePaths))
	for i, relFilePath := range relFilePaths {
		targetNames[i] = RelFilePathToTargetName(relFilePath)
	}
	return targetNames
}

// PkgNameToTargetName converts a package name to a Bazel target name that represents all targets in this package.
func PkgNameToTargetName(pkg string) string {
	if pkg == "." {
		pkg = ""
	}
	return "//" + pkg + ":*"
}

// GetContainingPackage returns the parent package of the given relative path.
// If the given path is a file, returned package will be the package containing the file.
// If the given path is a package itself, returned package will be the parent package of the directory.
// If the parent package is at workspace root, empty string is returned.
// If parent package does not exist (there is no guarantee that workspace root always contains BUILD file),
// then ErrParentPackageNotExist error is returned.
func GetContainingPackage(workspaceRoot string, relPath string) (string, error) {
	absDir := filepath.Dir(filepath.Join(workspaceRoot, relPath))
	for {
		for _, buildFileName := range _buildFileNames {
			buildFile := filepath.Join(absDir, buildFileName)
			s, err := os.Stat(buildFile)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("stat file for GetContainingPackage: %w", err)
			}
			if err == nil && !s.IsDir() {
				rel, err := filepath.Rel(workspaceRoot, absDir)
				if err != nil {
					return "", fmt.Errorf("computing relative path for GetContainingPackage: %w", err)
				}
				if rel == "." {
					rel = ""
				}
				return rel, nil
			}
		}

		if absDir == workspaceRoot {
			break
		}

		parentDir := filepath.Dir(absDir)
		if parentDir == absDir {
			return "", fmt.Errorf("unable to reach workspace root %q", workspaceRoot)
		}
		absDir = parentDir
	}
	return "", ErrParentPackageNotExist
}
