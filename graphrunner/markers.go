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

package graphrunner

import (
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// readRepoMarkerHashes reads Bazel's external repo marker files and returns
// a map from canonical repo name to a hash of the marker file's content.
// Each marker file captures the repository rule's inputs (URL, sha256,
// patches, etc.) and changes whenever the repo is upgraded.
func readRepoMarkerHashes(outputBase string) (map[string][]byte, error) {
	markerDir := filepath.Join(outputBase, "external")
	entries, err := os.ReadDir(markerDir)
	if err != nil {
		return nil, fmt.Errorf("read marker dir %s: %w", markerDir, err)
	}

	hashes := make(map[string][]byte)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".marker") {
			continue
		}
		repo := strings.TrimPrefix(strings.TrimSuffix(name, ".marker"), "@")
		if repo == "" {
			continue
		}

		content, err := os.ReadFile(filepath.Join(markerDir, name))
		if err != nil {
			return nil, fmt.Errorf("read marker for repo %s: %w", repo, err)
		}
		if len(content) == 0 {
			continue
		}
		h := sha1.Sum(content)
		hashes[repo] = h[:]
	}

	return hashes, nil
}
