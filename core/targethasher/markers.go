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
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/uber/tango/core/bazel"
)

// ReadRepoMarkerHashes resolves the Bazel output base, then reads the
// external repo marker files and returns a map from canonical repo name
// to a hash derived from the stable lines of each marker file.
// This hash changes whenever the repo's version, URL, or patch file
// contents change. ENV lines are excluded for cross-environment stability.
func ReadRepoMarkerHashes(ctx context.Context, workspacePath, bazelCommand string) (map[string][]byte, error) {
	outputBase, err := bazel.OutputBase(ctx, workspacePath, bazelCommand)
	if err != nil {
		return nil, fmt.Errorf("bazel output base: %w", err)
	}

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

		h, err := readMarkerHash(filepath.Join(markerDir, name))
		if err != nil {
			return nil, fmt.Errorf("read marker for repo %s: %w", repo, err)
		}
		if len(h) == 0 {
			continue
		}
		hashes[repo] = h
	}

	return hashes, nil
}

// readMarkerHash computes a hash from the stable lines of a marker file.
// The first line is a hash of the repo rule's declarative inputs (URL,
// version, patch paths) but does NOT include the content hashes of patch
// files. Those appear on FILE: lines alongside their SHA-256 content
// hashes. ENV: lines are skipped because environment variables can differ
// between CI environments and would cause unnecessary hash instability.
func readMarkerHash(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	h := newHash()
	hasContent := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "ENV:") {
			continue
		}
		h.Write([]byte(line))
		hasContent = true
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !hasContent {
		return nil, nil
	}
	return h.Sum(nil), nil
}
