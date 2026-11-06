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

// GetTGBGraphByTreeHash returns the cache path for the TGB-format target
// graph blob. strategy is part of the key because different computation
// strategies (e.g. SHELL vs NATIVE) can produce different graphs from the
// same tree state. excludeFilesRegex is folded into the key when non-empty
// (it affects computation). The "-tgb" suffix keeps the key distinct from the
// legacy gob blobs stored under the bare strategy segment.
func GetTGBGraphByTreeHash(repositoryID, treehash string, strategy entity.ComputationStrategy, excludeFilesRegex []string) string {
	path := filepath.Join(repositoryID, "graphs", treehash, strategy.String()+"-tgb")
	if hash := hashExcludeFilesRegex(excludeFilesRegex); hash != "" {
		path += "_requests-options-" + hash
	}
	return path
}

// GetTreehashCachePath returns the cache path for the treehash mapping.
// The git treehash is purely a function of git state (base SHA + applied
// requests), so neither excludeFilesRegex nor the computation strategy is
// part of this key.
func GetTreehashCachePath(repositoryID string, buildDescription entity.BuildDescription) string {
	path := filepath.Join(repositoryID, "treehashes", fmt.Sprintf("base-sha-%s", buildDescription.BaseSha))
	if len(buildDescription.ChangeRequests) > 0 {
		path += "_request-urls-" + url.GetReqURLsHash(buildDescription.ChangeRequests)
	}
	return path
}

// GetComparedTargetsCachePath returns the cache path for a compared target graph result.
// treehash1 and treehash2 are the resolved treehashes of the first and second revisions.
// repositoryID is the configured namespace shared by both revisions.
// excludeFilesRegex is folded into the key when non-empty (it affects computation).
// Empty ⇒ legacy path unchanged.
func GetComparedTargetsCachePath(repositoryID, treehash1, treehash2 string, excludeFilesRegex []string) string {
	path := filepath.Join(repositoryID, "compared-targets", treehash1+"_"+treehash2)
	if hash := hashExcludeFilesRegex(excludeFilesRegex); hash != "" {
		path += "_requests-options-" + hash
	}
	return path
}

// hashExcludeFilesRegex returns "" when excludeFilesRegex is empty (preserves
// legacy paths), otherwise the md5 hex digest of the sorted list. As new
// fields affecting computation are added, fold them into the digest here.
func hashExcludeFilesRegex(excludeFilesRegex []string) string {
	if len(excludeFilesRegex) == 0 {
		return ""
	}
	sorted := make([]string, len(excludeFilesRegex))
	copy(sorted, excludeFilesRegex)
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
