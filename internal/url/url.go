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

package url

import (
	"crypto/md5"
	"fmt"
	"sort"
	"strings"

	"github.com/uber/tango/entity"
)

// ToShortRemote returns the short remote name given a git ssh remote string.
// For example, "git@github:uber/tango" will return "uber/tango".
func ToShortRemote(remote string) string {
	strs := strings.Split(remote, ":")
	return strs[len(strs)-1]
}

// GetReqURLsHash returns a fixed-length MD5 hash of the sorted change request URLs.
// Each URL's bytes are fed into the digest individually (no separator)
func GetReqURLsHash(requests []entity.ChangeRequest) string {
	if len(requests) == 0 {
		return ""
	}
	urls := make([]string, 0, len(requests))
	for _, req := range requests {
		urls = append(urls, req.URL)
	}
	sort.Strings(urls)
	h := md5.New()
	for _, url := range urls {
		h.Write([]byte(url))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
