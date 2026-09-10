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
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/uber/tango/core/execcmd"
)

// readRepoMarkerHashes reads Bazel's external repo marker files and returns
// a map from canonical repo name to the marker's content hash. Each marker
// file's first line is a hex-encoded hash of the repository rule's inputs
// (URL, sha256, patches, etc.) that changes whenever the repo is upgraded.
func readRepoMarkerHashes(ctx context.Context, workspacePath, bazelCommand string) (map[string][]byte, error) {
	outputBase, err := bazelOutputBase(ctx, workspacePath, bazelCommand)
	if err != nil {
		return nil, err
	}

	markerDir := filepath.Join(outputBase, "external")
	entries, err := os.ReadDir(markerDir)
	if err != nil {
		return nil, err
	}

	hashes := make(map[string][]byte, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".marker") {
			continue
		}
		// Marker files are named @repo_name.marker — strip prefix and suffix.
		repo := strings.TrimPrefix(strings.TrimSuffix(name, ".marker"), "@")
		if repo == "" {
			continue
		}

		h, err := readMarkerFirstLine(filepath.Join(markerDir, name))
		if err != nil {
			continue
		}
		hashes[repo] = h
	}

	return hashes, nil
}

// readMarkerFirstLine reads the first line of a marker file and decodes
// the hex hash. Returns the raw bytes, or an error if the file can't be
// read or the first line isn't valid hex.
func readMarkerFirstLine(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return nil, scanner.Err()
	}
	line := strings.TrimSpace(scanner.Text())
	if line == "" {
		return nil, nil
	}
	return hex.DecodeString(line)
}

// bazelOutputBase runs `bazel info output_base` and returns the path.
func bazelOutputBase(ctx context.Context, workspacePath, bazelCommand string) (string, error) {
	if bazelCommand == "" {
		bazelCommand = "bazel"
	}
	cmd := execcmd.CommandContext(ctx, bazelCommand, "info", "output_base")
	cmd.Dir = workspacePath
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(out)), nil
}
