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
	"github.com/uber/tango/entity"
)

func TestGetGraphByTreeHash(t *testing.T) {
	t.Parallel()
	remote := "git@github:uber/tango"
	treehash := "abcd1234"
	strategy := entity.ComputationStrategyNative

	// Nil/empty exclude list ⇒ no suffix.
	got := GetGraphByTreeHash(remote, treehash, strategy, nil)
	assert.Equal(t, filepath.Join("uber/tango", "graphs", treehash, strategy.String()), got)
	assert.Equal(t, got, GetGraphByTreeHash(remote, treehash, strategy, []string{}))

	// Different strategies ⇒ different keys.
	assert.NotEqual(t, got, GetGraphByTreeHash(remote, treehash, entity.ComputationStrategyShell, nil))

	// Non-empty list ⇒ suffix appended; different lists ⇒ different keys.
	withFoo := GetGraphByTreeHash(remote, treehash, strategy, []string{"foo.*"})
	assert.NotEqual(t, got, withFoo)
	assert.NotEqual(t, withFoo, GetGraphByTreeHash(remote, treehash, strategy, []string{"bar.*"}))
	// Order-independence: sort before hashing.
	assert.Equal(t,
		GetGraphByTreeHash(remote, treehash, strategy, []string{"a", "b"}),
		GetGraphByTreeHash(remote, treehash, strategy, []string{"b", "a"}),
	)
}

func TestGetTreehashCachePath(t *testing.T) {
	t.Parallel()
	desc := entity.BuildDescription{
		Remote:  "git@github:uber/tango",
		BaseSha: "deadbeef",
		ChangeRequests: []entity.ChangeRequest{
			{URL: "github://org/repo/pull/1"},
			{URL: "custom://foo/bar"},
		},
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
		in   []entity.ChangeRequest
		want string
	}{
		{
			name: "empty",
			in:   []entity.ChangeRequest{},
			want: "",
		},
		{
			name: "single",
			in:   []entity.ChangeRequest{{URL: "github://org/repo/pull/42"}},
			want: md5hex("github://org/repo/pull/42"),
		},
		{
			name: "multiple",
			in:   []entity.ChangeRequest{{URL: "a"}, {URL: "b"}},
			want: md5hex("a", "b"),
		},
		{
			name: "multiple sorted",
			in:   []entity.ChangeRequest{{URL: "b"}, {URL: "a"}},
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

	// Nil/empty list ⇒ legacy path.
	assert.Equal(t, got, GetComparedTargetsCachePath("git@github:uber/tango", "abc", "def", []string{}))

	// Different exclude lists ⇒ different keys.
	assert.NotEqual(t, got, GetComparedTargetsCachePath("git@github:uber/tango", "abc", "def", []string{"foo.*"}))
}
