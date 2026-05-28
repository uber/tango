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

package common

import (
	"crypto/md5"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/targethasher"
	pb "github.com/uber/tango/tangopb"
)

func TestToShortRemote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		remote string
		want   string
	}{
		{
			name:   "ssh remote with host",
			remote: "git@github:uber/tango",
			want:   "uber/tango",
		},
		{
			name:   "already short",
			remote: "uber/tango",
			want:   "uber/tango",
		},
		{
			name:   "with nested path",
			remote: "git@github:org/project/sub",
			want:   "org/project/sub",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ToShortRemote(tt.remote))
		})
	}
}

func TestGetGraphByTreeHash(t *testing.T) {
	t.Parallel()
	remote := "git@github:uber/tango"
	treehash := "abcd1234"

	// Nil/empty options ⇒ legacy path (regression: cache compatibility).
	got := GetGraphByTreeHash(remote, treehash, nil)
	assert.Equal(t, filepath.Join("graph", "uber/tango", treehash), got)
	assert.Equal(t, got, GetGraphByTreeHash(remote, treehash, &pb.RequestOptions{}))

	// Non-empty options ⇒ suffix appended; different lists ⇒ different keys.
	withFoo := GetGraphByTreeHash(remote, treehash, &pb.RequestOptions{ExtraExcludeFilesRegex: []string{"foo.*"}})
	assert.NotEqual(t, got, withFoo)
	assert.NotEqual(t, withFoo, GetGraphByTreeHash(remote, treehash, &pb.RequestOptions{ExtraExcludeFilesRegex: []string{"bar.*"}}))
	// Order-independence: sort before hashing.
	assert.Equal(t,
		GetGraphByTreeHash(remote, treehash, &pb.RequestOptions{ExtraExcludeFilesRegex: []string{"a", "b"}}),
		GetGraphByTreeHash(remote, treehash, &pb.RequestOptions{ExtraExcludeFilesRegex: []string{"b", "a"}}),
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
	want := filepath.Join("treehash", "uber/tango", "treehash-map-deadbeef", fmt.Sprintf("%x", h.Sum(nil))) + "-" + pb.COMPUTATION_STRATEGY_INVALID.String()
	assert.Equal(t, want, got)
}

func TestGetReqsHash(t *testing.T) {
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
			assert.Equal(t, tt.want, GetReqsHash(tt.in))
		})
	}
}

func TestGetComparedTargetsCachePath(t *testing.T) {
	t.Parallel()
	got := GetComparedTargetsCachePath("git@github:uber/tango", "abc", "def", nil)
	assert.Equal(t, filepath.Join("compared-targets", "uber/tango", "abc", "def"), got)

	// Nil/empty options ⇒ legacy path.
	assert.Equal(t, got, GetComparedTargetsCachePath("git@github:uber/tango", "abc", "def", &pb.RequestOptions{}))

	// Different exclude lists ⇒ different keys.
	assert.NotEqual(t, got, GetComparedTargetsCachePath("git@github:uber/tango", "abc", "def", &pb.RequestOptions{ExtraExcludeFilesRegex: []string{"foo.*"}}))
}

func TestGetChangedTargetsAndEdgesCachePath(t *testing.T) {
	t.Parallel()
	got := GetChangedTargetsAndEdgesCachePath("git@github:uber/tango", "abc", "def", nil)
	assert.Equal(t, filepath.Join("compared-targets-and-edges", "uber/tango", "abc", "def"), got)

	// Must be distinct from the GetChangedTargets cache path.
	assert.NotEqual(t, GetComparedTargetsCachePath("git@github:uber/tango", "abc", "def", nil), got)

	// Nil/empty options ⇒ legacy path; non-empty ⇒ different key.
	assert.Equal(t, got, GetChangedTargetsAndEdgesCachePath("git@github:uber/tango", "abc", "def", &pb.RequestOptions{}))
	assert.NotEqual(t, got, GetChangedTargetsAndEdgesCachePath("git@github:uber/tango", "abc", "def", &pb.RequestOptions{ExtraExcludeFilesRegex: []string{"foo.*"}}))
}

func TestChunkTargets(t *testing.T) {
	t.Parallel()

	// Create 250 targets, chunk by 100 → expect 3 chunks (100, 100, 50)
	targets := make([]*pb.OptimizedTarget, 250)
	for i := range targets {
		targets[i] = &pb.OptimizedTarget{Id: int32(i)}
	}

	responses := chunkTargets(targets, 100)

	require.Len(t, responses, 3)

	// Verify total count and order preserved
	var total int
	for _, resp := range responses {
		item := resp.Item.(*pb.GetTargetGraphResponse_Targets)
		for _, target := range item.Targets.Targets {
			assert.Equal(t, int32(total), target.Id)
			total++
		}
	}
	assert.Equal(t, 250, total)
}

func TestResultToGetTargetGraphResponse_Chunking(t *testing.T) {
	t.Parallel()

	// 500 targets with DefaultTargetChunkSize=250 → 2 target chunks + 1 metadata = 3 responses
	numTargets := 500
	result := targethasher.Result{
		TargetNames: make([]string, numTargets),
		Targets:     make(map[string]*targethasher.Target, numTargets),
	}
	for i := 0; i < numTargets; i++ {
		name := fmt.Sprintf("//pkg:target%d", i)
		result.TargetNames[i] = name
		result.Targets[name] = &targethasher.Target{Name: name, Hash: []byte{0}, RuleType: "go_library"}
	}

	responses, err := ResultToGetTargetGraphResponse(result)
	require.NoError(t, err)

	// 2 target chunks + 1 metadata chunk (500 targets well under DefaultMetadataMapChunkSize)
	require.Len(t, responses, 3)

	for _, resp := range responses[:2] {
		_, ok := resp.Item.(*pb.GetTargetGraphResponse_Targets)
		assert.True(t, ok, "expected Targets chunk")
	}
	_, ok := responses[2].Item.(*pb.GetTargetGraphResponse_Metadata)
	assert.True(t, ok, "last response should be Metadata")
}
