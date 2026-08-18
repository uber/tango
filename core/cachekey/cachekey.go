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

// Package cachekey builds the storage keys used to cache target graphs, treehashes, and compared-target-graph results.
package cachekey

import (
	"crypto/md5"
	"fmt"
	"hash"
	"io"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/url"
)

// GetGraphByTreeHash returns the cache path for the target graph by treehash.
// strategy is part of the key because different computation strategies (e.g.
// SHELL vs NATIVE) can produce different graphs from the same tree state.
// excludeFilesRegex is folded into the key when non-empty (it affects
// computation). Empty ⇒ legacy path unchanged.
func GetGraphByTreeHash(remote, treehash string, strategy entity.ComputationStrategy, excludeFilesRegex []string) string {
	path := filepath.Join(url.ToShortRemote(remote), "graphs", treehash, strategy.String())
	if hash := hashSortedStrings(excludeFilesRegex); hash != "" {
		path += "_requests-options-" + hash
	}
	return path
}

// GetTGBGraphByTreeHash returns the cache path for the TGB-format target
// graph blob. It is the GetGraphByTreeHash key with a "-tgb" suffix on the
// strategy segment, so the two formats never share a key: binaries that
// predate TGB never construct this path and therefore never see a TGB blob,
// and a GraphFormat flip changes which keys are consulted, never how an
// existing blob is interpreted. The suffix keeps the key a sibling of the
// gob key rather than a child — on the disk backend a key is a file, and a
// child key would need the gob file to be a directory.
func GetTGBGraphByTreeHash(remote, treehash string, strategy entity.ComputationStrategy, excludeFilesRegex []string) string {
	path := filepath.Join(url.ToShortRemote(remote), "graphs", treehash, strategy.String()+"-tgb")
	if hash := hashSortedStrings(excludeFilesRegex); hash != "" {
		path += "_requests-options-" + hash
	}
	return path
}

// GetTreehashCachePath returns the cache path for the treehash mapping.
// The git treehash is purely a function of git state (base SHA + applied
// requests), so neither excludeFilesRegex nor the computation strategy is
// part of this key.
func GetTreehashCachePath(buildDescription entity.BuildDescription) string {
	path := filepath.Join(url.ToShortRemote(buildDescription.Remote), "treehashes", fmt.Sprintf("base-sha-%s", buildDescription.BaseSha))
	if len(buildDescription.ChangeRequests) > 0 {
		path += "_request-urls-" + url.GetReqURLsHash(buildDescription.ChangeRequests)
	}
	return path
}

// GetComparedTargetsCachePath returns the cache path for a compared target graph result.
// treehash1 and treehash2 are the resolved treehashes of the first and second revisions.
// remote is the shared git remote for both revisions.
// excludeFilesRegex is folded into the key when non-empty (it affects computation).
// Empty ⇒ legacy path unchanged.
func GetComparedTargetsCachePath(remote, treehash1, treehash2 string, excludeFilesRegex []string) string {
	path := filepath.Join(url.ToShortRemote(remote), "compared-targets", treehash1+"_"+treehash2)
	if hash := hashSortedStrings(excludeFilesRegex); hash != "" {
		path += "_requests-options-" + hash
	}
	return path
}

// GetAllTargetsChangedCachePath returns the cache path for a cached
// AllTargetsFiles-trigger check result between two revisions' treehashes.
// allTargetsFiles is folded into the key because it's the repository config
// input that determines the result; a config change must not read a stale
// answer computed under a different file list.
func GetAllTargetsChangedCachePath(remote, treehash1, treehash2 string, allTargetsFiles []string) string {
	path := filepath.Join(url.ToShortRemote(remote), "all-targets-changed", treehash1+"_"+treehash2)
	if hash := hashSortedStrings(allTargetsFiles); hash != "" {
		path += "_files-" + hash
	}
	return path
}

// hashSortedStrings returns "" when values is empty (preserves legacy
// paths), otherwise the md5 hex digest of the sorted list. As new fields
// affecting computation are added, fold them into the digest here.
func hashSortedStrings(values []string) string {
	if len(values) == 0 {
		return ""
	}
	sorted := make([]string, len(values))
	copy(sorted, values)
	sort.Strings(sorted)
	h := md5.New()
	for _, r := range sorted {
		writeFramedString(h, r)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func writeFramedString(h hash.Hash, value string) {
	io.WriteString(h, strconv.Itoa(len(value)))
	h.Write([]byte{':'})
	io.WriteString(h, value)
}
