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

package changeuri

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

const _sha = "c3a4b5d6e7f80912a3b4c5d6e7f80912a3b4c5d6"

func TestParse_Valid(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want PullRequest
	}{
		{
			name: "simple",
			in:   "github://github.com/uber/tango/pull/123/" + _sha,
			want: PullRequest{Host: "github.com", Org: "uber", Repo: "tango", Number: "123", HeadSHA: _sha},
		},
		{
			name: "internal host",
			in:   "github://github.uberinternal.com/uber/submitqueue/pull/1/" + _sha,
			want: PullRequest{Host: "github.uberinternal.com", Org: "uber", Repo: "submitqueue", Number: "1", HeadSHA: _sha},
		},
		{
			name: "host with port",
			in:   "github://github.example.com:8443/org/repo/pull/42/" + _sha,
			want: PullRequest{Host: "github.example.com:8443", Org: "org", Repo: "repo", Number: "42", HeadSHA: _sha},
		},
		{
			name: "nested org",
			in:   "github://github.com/uber/frontend/webapp/pull/7/" + _sha,
			want: PullRequest{Host: "github.com", Org: "uber/frontend", Repo: "webapp", Number: "7", HeadSHA: _sha},
		},
		{
			name: "org segment literally named pull",
			in:   "github://github.com/org/pull/repo/pull/9/" + _sha,
			want: PullRequest{Host: "github.com", Org: "org/pull", Repo: "repo", Number: "9", HeadSHA: _sha},
		},
		{
			name: "case-sensitive org and repo kept verbatim",
			in:   "github://github.com/Uber/Tango/pull/5/" + _sha,
			want: PullRequest{Host: "github.com", Org: "Uber", Repo: "Tango", Number: "5", HeadSHA: _sha},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.in, got.String(), "round-trip must be byte-for-byte")
		})
	}
}

func TestParse_Invalid(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "no scheme", in: "github.com/uber/tango/pull/123/" + _sha},
		{name: "wrong scheme", in: "phab://phabricator.example.com/D12345/67890"},
		{name: "https web URL", in: "https://github.com/uber/tango/pull/123"},
		{name: "missing host", in: "github:///uber/tango/pull/123/" + _sha},
		{name: "legacy hostless form", in: "github://uber/tango/pull/123"},
		{name: "uppercase host", in: "github://GitHub.com/uber/tango/pull/123/" + _sha},
		{name: "uppercase scheme", in: "GITHUB://github.com/uber/tango/pull/123/" + _sha},
		{name: "userinfo", in: "github://user@github.com/uber/tango/pull/123/" + _sha},
		{name: "empty port", in: "github://github.com:/uber/tango/pull/123/" + _sha},
		{name: "non-numeric port", in: "github://github.com:abc/uber/tango/pull/123/" + _sha},
		{name: "query", in: "github://github.com/uber/tango/pull/123/" + _sha + "?x=1"},
		{name: "bare question mark", in: "github://github.com/uber/tango/pull/123/" + _sha + "?"},
		{name: "fragment", in: "github://github.com/uber/tango/pull/123/" + _sha + "#frag"},
		{name: "missing head sha", in: "github://github.com/uber/tango/pull/123"},
		{name: "short sha", in: "github://github.com/uber/tango/pull/123/c3a4b5d"},
		{name: "uppercase sha", in: "github://github.com/uber/tango/pull/123/C3A4B5D6E7F80912A3B4C5D6E7F80912A3B4C5D6"},
		{name: "non-hex sha", in: "github://github.com/uber/tango/pull/123/z3a4b5d6e7f80912a3b4c5d6e7f80912a3b4c5d6"},
		{name: "zero pr number", in: "github://github.com/uber/tango/pull/0/" + _sha},
		{name: "leading-zero pr number", in: "github://github.com/uber/tango/pull/0123/" + _sha},
		{name: "non-numeric pr number", in: "github://github.com/uber/tango/pull/12a/" + _sha},
		{name: "missing pull segment", in: "github://github.com/uber/tango/123/" + _sha},
		{name: "missing repo", in: "github://github.com/uber/pull/123/" + _sha},
		{name: "trailing slash", in: "github://github.com/uber/tango/pull/123/" + _sha + "/"},
		{name: "empty path segment", in: "github://github.com/uber//tango/pull/123/" + _sha},
		{name: "no path", in: "github://github.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.in)
			require.Error(t, err)
		})
	}
}

func TestParse_RoundTrip(t *testing.T) {
	segment := rapid.StringMatching(`[A-Za-z0-9][A-Za-z0-9._-]{0,20}`)
	rapid.Check(t, func(t *rapid.T) {
		p := PullRequest{
			Host:    rapid.StringMatching(`[a-z0-9.-]{1,30}(:[1-9][0-9]{0,4})?`).Draw(t, "host"),
			Org:     segment.Draw(t, "org"),
			Repo:    segment.Draw(t, "repo"),
			Number:  strconv.Itoa(rapid.IntRange(1, 1<<30).Draw(t, "number")),
			HeadSHA: rapid.StringMatching(`[0-9a-f]{40}`).Draw(t, "sha"),
		}
		parsed, err := Parse(p.String())
		if err != nil {
			// Some generated hosts are not valid URI authorities (e.g. a
			// bare "-"); rejection is fine, silent mutation is not.
			return
		}
		require.Equal(t, p.String(), parsed.String(), "parse then serialize must round-trip byte-for-byte")
		require.Equal(t, p.Number, parsed.Number)
		require.Equal(t, p.HeadSHA, parsed.HeadSHA)
	})
}
