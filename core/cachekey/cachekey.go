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

	"github.com/uber/tango/config"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/url"
)

// GetGraphByTreeHash returns the cache path for the target graph by treehash.
// strategy is part of the key because different computation strategies (e.g.
// SHELL vs NATIVE) can produce different graphs from the same tree state.
// excludeFilesRegex is folded into the key when non-empty (it affects
// computation). Empty => legacy path unchanged.
// configHash is the md5 hex digest of graph-affecting RepositoryConfig fields
// (see HashGraphAffectingConfig). Empty => legacy path unchanged, which is the
// correct result when the config equals the zero/default value.
func GetGraphByTreeHash(remote, treehash string, strategy entity.ComputationStrategy, excludeFilesRegex []string, configHash string) string {
	path := filepath.Join(url.ToShortRemote(remote), "graphs", treehash, strategy.String())
	if hash := hashExcludeFilesRegex(excludeFilesRegex); hash != "" {
		path += "_requests-options-" + hash
	}
	if configHash != "" {
		path += "_repo-config-" + configHash
	}
	return path
}

// GetTreehashCachePath returns the cache path for the treehash mapping.
// The git treehash is purely a function of git state (base SHA + applied
// requests), so neither excludeFilesRegex nor the computation strategy is
// part of this key.
//
// IMPORTANT: this mapping is only safe to use when base_sha is a full 40-hex
// SHA (see IsFullHexSHA). Mutable refs (HEAD, branch names, short SHAs) can
// move after the mapping was written, making a cached treehash stale. Callers
// must check IsFullHexSHA before reading or writing this mapping and skip the
// cache for mutable refs so the workspace is materialized and the ref resolved
// fresh each time.
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
// Empty => legacy path unchanged.
func GetComparedTargetsCachePath(remote, treehash1, treehash2 string, excludeFilesRegex []string) string {
	path := filepath.Join(url.ToShortRemote(remote), "compared-targets", treehash1+"_"+treehash2)
	if hash := hashExcludeFilesRegex(excludeFilesRegex); hash != "" {
		path += "_requests-options-" + hash
	}
	return path
}

// IsFullHexSHA reports whether s is exactly 40 hexadecimal characters,
// matching the format of a full git SHA-1 object hash. Both lowercase and
// uppercase hex digits are accepted. Use this to decide whether a base_sha is
// safe for treehash mapping cache lookups; mutable refs (HEAD, branch names,
// tags, short SHAs) should skip the mapping so the ref is resolved fresh.
func IsFullHexSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// HashGraphAffectingConfig returns the md5 hex digest of the RepositoryConfig
// fields that affect graph computation (query construction and target hashing).
// Returns "" when every graph-affecting field is at its zero value, preserving
// legacy cache keys for default-config deployments.
//
// Graph-affecting fields: BzlmodEnabled, BazelCommand, BazelExtraArgs,
// BazelStartupOptions, FullHashRepos, ExcludeExternalTargets, ExcludedFiles.
// Non-graph fields (Remote, QueryTimeout, StreamBazelLogs) are excluded
// because they do not change the computed target graph.
func HashGraphAffectingConfig(cfg config.RepositoryConfig) string {
	if !cfg.BzlmodEnabled &&
		!cfg.ExcludeExternalTargets &&
		cfg.BazelCommand == "" &&
		len(cfg.BazelExtraArgs) == 0 &&
		len(cfg.BazelStartupOptions) == 0 &&
		len(cfg.FullHashRepos) == 0 &&
		len(cfg.ExcludedFiles) == 0 {
		return ""
	}
	h := md5.New()
	// Booleans: write a fixed token only when true so that
	// false (the zero value) contributes nothing.
	if cfg.BzlmodEnabled {
		writeFramedString(h, "bzlmod_enabled")
	}
	if cfg.ExcludeExternalTargets {
		writeFramedString(h, "exclude_external_targets")
	}
	// Scalar string.
	writeFramedString(h, cfg.BazelCommand)
	// Sorted slices for order-independence.
	writeSortedSlice(h, "bazel_extra_args", cfg.BazelExtraArgs)
	writeSortedSlice(h, "bazel_startup_options", cfg.BazelStartupOptions)
	writeSortedSlice(h, "full_hash_repos", cfg.FullHashRepos)
	writeSortedSlice(h, "excluded_files", cfg.ExcludedFiles)
	return fmt.Sprintf("%x", h.Sum(nil))
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

// writeSortedSlice writes a labelled, length-prefixed, sorted list of strings
// into the hash so that order of elements does not affect the digest.
func writeSortedSlice(h hash.Hash, label string, values []string) {
	writeFramedString(h, label)
	sorted := make([]string, len(values))
	copy(sorted, values)
	sort.Strings(sorted)
	writeFramedString(h, strconv.Itoa(len(sorted)))
	for _, v := range sorted {
		writeFramedString(h, v)
	}
}
