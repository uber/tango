// Copyright (c) 2026 Uber Technologies, Inc.
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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testDigest     = "fbbfccf5194415fded38959668a5627efc0e02be1bbaa6420edf3c25c057b11a"
	testPatchLine  = "FILE:@@//patches/protobuf/use_prebuilt_protoc.patch 0f4b98e755d6ad0cee62a8fe8ce36d02fe129936301a34fa59abe9bbd9945a43"
	testGoEnvLine  = "FILE:@@gazelle++non_module_deps+bazel_gazelle_go_repository_cache//go.env 860dcdd7df8b260d298aa300fed108cda552d3e5420f2565fb742c3183025dbb"
	testToolBinary = "FILE:@@gazelle++non_module_deps+bazel_gazelle_go_repository_tools//bin/gazelle 3c218b37322806fdfa4b7e6a090d7fd5db919b07df3dde930dc029c1854a9989"
	testSourceFile = "FILE:@@gazelle+//BUILD.bazel eda52fca39edfb09f3c35adf12ad3bde86f4f92a7586b59eebfd3aea"
	testMapping    = "REPO_MAPPING:gazelle+,bazel_tools bazel_tools"
	testEnv        = "ENV:GOPATH /Users/someone/go-code"
)

func markerHash(t *testing.T, lines ...string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repo.marker")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	h, err := readMarkerHash(path)
	require.NoError(t, err)
	return h
}

func TestReadMarkerHash_IgnoresNonLocalAndEnvLines(t *testing.T) {
	base := markerHash(t, testDigest, testPatchLine)

	// Same digest and patch, but with every kind of line that must not count,
	// including generated files whose hashes differ between workspaces.
	withNoise := markerHash(t,
		testDigest,
		testGoEnvLine,
		testToolBinary,
		testSourceFile,
		testPatchLine,
		testMapping,
		testEnv,
	)
	assert.Equal(t, base, withNoise)

	otherWorkspace := markerHash(t,
		testDigest,
		strings.Replace(testGoEnvLine, "860dcdd7", "a0b4be1a", 1),
		strings.Replace(testToolBinary, "3c218b37", "fcfe8347", 1),
		testPatchLine,
	)
	assert.Equal(t, base, otherWorkspace, "path-dependent FILE: lines from other repos must not affect the hash")
}

func TestReadMarkerHash_ChangesOnRuleInputs(t *testing.T) {
	base := markerHash(t, testDigest, testPatchLine)
	bumped := markerHash(t, strings.Replace(testDigest, "fbbfccf5", "00000000", 1), testPatchLine)
	assert.NotEqual(t, base, bumped, "first line (repo rule inputs) must affect the hash")
}

func TestReadMarkerHash_ChangesOnPatchContent(t *testing.T) {
	base := markerHash(t, testDigest, testPatchLine)
	edited := markerHash(t, testDigest, strings.Replace(testPatchLine, "0f4b98e7", "ffffffff", 1))
	assert.NotEqual(t, base, edited, "main-repo FILE: lines (patches) must affect the hash")

	added := markerHash(t, testDigest, testPatchLine, "FILE:@@//patches/protobuf/extra.patch 1111111111111111111111111111111111111111111111111111111111111111")
	assert.NotEqual(t, base, added, "adding a patch must affect the hash")
}

func TestReadMarkerHash_Empty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.marker")
	require.NoError(t, os.WriteFile(path, []byte("\n  \n"), 0o644))
	h, err := readMarkerHash(path)
	require.NoError(t, err)
	assert.Nil(t, h)
}
