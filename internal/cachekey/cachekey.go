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
	"path/filepath"
	"sort"

	"github.com/uber/tango/core/common"
	"github.com/uber/tango/tangopb"
)

// GetGraphByTreeHash returns the cache path for the target graph by treehash.
// strategy is part of the key because different computation strategies (e.g.
// SHELL vs NATIVE) can produce different graphs from the same tree state.
// requestOptions is folded into the key when any of its fields affect computation
// (today: extra_exclude_files_regex). Empty/nil ⇒ legacy path unchanged.
func GetGraphByTreeHash(remote, treehash string, strategy tangopb.ComputationStrategy, requestOptions *tangopb.RequestOptions) string {
	path := filepath.Join(common.ToShortRemote(remote), "graphs", treehash, strategy.String())
	if hash := hashRequestOptions(requestOptions); hash != "" {
		path += "_requests-options-" + hash
	}
	return path
}

// GetTreehashCachePath returns the cache path for the treehash mapping.
// The git treehash is purely a function of git state (base SHA + applied
// requests), so neither requestOptions nor the computation strategy is part
// of this key.
func GetTreehashCachePath(buildDescription *tangopb.BuildDescription) string {
	path := filepath.Join(common.ToShortRemote(buildDescription.Remote), "treehashes", fmt.Sprintf("base-sha-%s", buildDescription.BaseSha))
	if len(buildDescription.Requests) > 0 {
		path += "_request-urls-" + getReqURLsHash(buildDescription.Requests)
	}
	return path
}

// GetComparedTargetsCachePath returns the cache path for a compared target graph result.
// treehash1 and treehash2 are the resolved treehashes of the first and second revisions.
// remote is the shared git remote for both revisions.
// requestOptions is folded into the key when any of its fields affect computation.
// Empty/nil ⇒ legacy path unchanged.
func GetComparedTargetsCachePath(remote, treehash1, treehash2 string, requestOptions *tangopb.RequestOptions) string {
	path := filepath.Join(common.ToShortRemote(remote), "compared-targets", treehash1+"_"+treehash2)
	if hash := hashRequestOptions(requestOptions); hash != "" {
		path += "_requests-options-" + hash
	}
	return path
}

// getReqURLsHash returns a fixed-length MD5 hash of the sorted request URLs.
// Each URL's bytes are fed into the digest individually (no separator)
func getReqURLsHash(requests []*tangopb.Request) string {
	if len(requests) == 0 {
		return ""
	}
	urls := make([]string, 0, len(requests))
	for _, req := range requests {
		urls = append(urls, req.GetUrl())
	}
	sort.Strings(urls)
	h := md5.New()
	for _, url := range urls {
		h.Write([]byte(url))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// hashRequestOptions returns "" when no field of RequestOptions contributes to
// the cache (preserves legacy paths), otherwise the md5 hex digest of those
// fields. As new fields are added to RequestOptions, fold them into the digest
// here.
func hashRequestOptions(opts *tangopb.RequestOptions) string {
	if opts == nil {
		return ""
	}
	excludes := opts.GetExtraExcludeFilesRegex()
	if len(excludes) == 0 {
		return ""
	}
	sorted := make([]string, len(excludes))
	copy(sorted, excludes)
	sort.Strings(sorted)
	h := md5.New()
	for _, r := range sorted {
		h.Write([]byte(r))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
