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

package cachekey

import (
	"crypto/md5"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	pb "github.com/uber/tango/tangopb"
)

func TestGetGraphByTreeHash(t *testing.T) {
	t.Parallel()
	remote := "git@github:uber/tango"
	treehash := "abcd1234"
	strategy := pb.COMPUTATION_STRATEGY_NATIVE

	// Nil/empty options ⇒ no request-options suffix.
	got := GetGraphByTreeHash(remote, treehash, strategy, nil)
	assert.Equal(t, filepath.Join("uber/tango", "graphs", treehash, strategy.String()), got)
	assert.Equal(t, got, GetGraphByTreeHash(remote, treehash, strategy, &pb.RequestOptions{}))

	// Different strategies ⇒ different keys.
	assert.NotEqual(t, got, GetGraphByTreeHash(remote, treehash, pb.COMPUTATION_STRATEGY_SHELL, nil))

	// Non-empty options ⇒ suffix appended; different lists ⇒ different keys.
	withFoo := GetGraphByTreeHash(remote, treehash, strategy, &pb.RequestOptions{ExtraExcludeFilesRegex: []string{"foo.*"}})
	assert.NotEqual(t, got, withFoo)
	assert.NotEqual(t, withFoo, GetGraphByTreeHash(remote, treehash, strategy, &pb.RequestOptions{ExtraExcludeFilesRegex: []string{"bar.*"}}))
	// Order-independence: sort before hashing.
	assert.Equal(t,
		GetGraphByTreeHash(remote, treehash, strategy, &pb.RequestOptions{ExtraExcludeFilesRegex: []string{"a", "b"}}),
		GetGraphByTreeHash(remote, treehash, strategy, &pb.RequestOptions{ExtraExcludeFilesRegex: []string{"b", "a"}}),
	)
}

func TestGetTreehashCachePath(t *testing.T) {
	t.Parallel()
	reqs := []*pb.Request{
		{Url: "github://org/repo/pull/1"},
		{Url: "custom://foo/bar"},
	}
	desc := &pb.BuildDescription{
		Remote:   "git@github:uber/tango",
		BaseSha:  "deadbeef",
		Requests: reqs,
	}
	got := GetTreehashCachePath(desc)
	// URLs are sorted then fed individually into the digest (no separator)
	h := md5.New()
	h.Write([]byte("custom://foo/bar"))
	h.Write([]byte("github://org/repo/pull/1"))
	want := filepath.Join("uber/tango", "treehashes", "base-sha-deadbeef") + "_request-urls-" + fmt.Sprintf("%x", h.Sum(nil))
	assert.Equal(t, want, got)
}

func TestGetReqURLsHash(t *testing.T) {
	t.Parallel()
	md5hex := func(strs ...string) string {
		h := md5.New()
		for _, s := range strs {
			h.Write([]byte(s))
		}
		return fmt.Sprintf("%x", h.Sum(nil))
	}
	tests := []struct {
		name string
		in   []*pb.Request
		want string
	}{
		{
			name: "empty",
			in:   []*pb.Request{},
			want: "",
		},
		{
			name: "single",
			in:   []*pb.Request{{Url: "github://org/repo/pull/42"}},
			want: md5hex("github://org/repo/pull/42"),
		},
		{
			name: "multiple",
			in:   []*pb.Request{{Url: "a"}, {Url: "b"}},
			want: md5hex("a", "b"),
		},
		{
			name: "multiple sorted",
			in:   []*pb.Request{{Url: "b"}, {Url: "a"}},
			want: md5hex("a", "b"),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, getReqURLsHash(tt.in))
		})
	}
}

func TestGetComparedTargetsCachePath(t *testing.T) {
	t.Parallel()
	got := GetComparedTargetsCachePath("git@github:uber/tango", "abc", "def", nil)
	assert.Equal(t, filepath.Join("uber/tango", "compared-targets", "abc_def"), got)

	// Nil/empty options ⇒ legacy path.
	assert.Equal(t, got, GetComparedTargetsCachePath("git@github:uber/tango", "abc", "def", &pb.RequestOptions{}))

	// Different exclude lists ⇒ different keys.
	assert.NotEqual(t, got, GetComparedTargetsCachePath("git@github:uber/tango", "abc", "def", &pb.RequestOptions{ExtraExcludeFilesRegex: []string{"foo.*"}}))
}
