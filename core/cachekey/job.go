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

package cachekey

import (
	"crypto/md5"
	"fmt"
	"hash"
	"path/filepath"
	"strconv"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/url"
)

// GetTargetGraphJobToken returns the job token, a digest of the computation inputs and bypass_cache.
func GetTargetGraphJobToken(req entity.GetTargetGraphRequest) string {
	h := md5.New()
	writeBuildDescription(h, req.Build)
	writeExcludeFilesRegex(h, req.ExcludeFilesRegex)
	writeBool(h, req.BypassCache)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// GetChangedTargetsJobToken returns the job token, a digest of the computation inputs and bypass_cache.
func GetChangedTargetsJobToken(first, second entity.BuildDescription, excludeFilesRegex []string, bypassCache bool) string {
	h := md5.New()
	writeBuildDescription(h, first)
	writeBuildDescription(h, second)
	writeExcludeFilesRegex(h, excludeFilesRegex)
	writeBool(h, bypassCache)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// GetTargetGraphJobPath returns the job status key of a target-graph job token.
func GetTargetGraphJobPath(token string) string {
	return filepath.Join("jobs", "target-graph", token)
}

// GetChangedTargetsJobPath returns the job status key of a changed-targets job token.
func GetChangedTargetsJobPath(token string) string {
	return filepath.Join("jobs", "changed-targets", token)
}

func writeBuildDescription(h hash.Hash, desc entity.BuildDescription) {
	writeFramedString(h, desc.Remote)
	writeFramedString(h, desc.BaseSha)
	writeFramedString(h, desc.Strategy.String())
	writeFramedString(h, url.GetReqURLsHash(desc.ChangeRequests))
}

func writeBool(h hash.Hash, b bool) {
	writeFramedString(h, strconv.FormatBool(b))
}
