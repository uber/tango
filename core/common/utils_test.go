package common

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"testing"

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
			got := ToShortRemote(tt.remote)
			if got != tt.want {
				t.Fatalf("ToShortRemote(%q) = %q, want %q", tt.remote, got, tt.want)
			}
		})
	}
}

func TestGetGraphByTreeHash(t *testing.T) {
	t.Parallel()
	remote := "git@github:uber/tango"
	treehash := "abcd1234"
	got := GetGraphByTreeHash(remote, treehash)
	want := filepath.Join("uber/tango", treehash)
	if got != want {
		t.Fatalf("GetGraphByTreeHash(%q,%q) = %q, want %q", remote, treehash, got, want)
	}
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
	// Build expected with the same encoding semantics used by the implementation
	encoded := []string{
		base64.RawURLEncoding.EncodeToString([]byte(reqs[0].Url)),
		base64.RawURLEncoding.EncodeToString([]byte(reqs[1].Url)),
	}
	want := filepath.Join("uber/tango", "deadbeef", encoded[0]+"-"+encoded[1], pb.COMPUTATION_STRATEGY_INVALID.String())
	if got != want {
		t.Fatalf("GetTreehashCachePath(..) = %q, want %q", got, want)
	}
}

func TestGetReqsBase64(t *testing.T) {
	t.Parallel()
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
			want: base64.RawURLEncoding.EncodeToString([]byte("github://org/repo/pull/42")),
		},
		{
			name: "multiple",
			in:   []*pb.Request{{Url: "a"}, {Url: "b"}},
			want: base64.RawURLEncoding.EncodeToString([]byte("a")) + "-" + base64.RawURLEncoding.EncodeToString([]byte("b")),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := getReqsBase64(tt.in)
			if got != tt.want {
				t.Fatalf("getReqsBase64(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestChunkTargets(t *testing.T) {
	t.Parallel()

	// Create 250 targets, chunk by 100 → expect 3 chunks (100, 100, 50)
	targets := make([]*pb.OptimizedTarget, 250)
	for i := range targets {
		targets[i] = &pb.OptimizedTarget{Id: int32(i)}
	}

	responses := chunkTargets(targets, 100)

	if len(responses) != 3 {
		t.Fatalf("got %d chunks, want 3", len(responses))
	}

	// Verify total count and order preserved
	var total int
	for _, resp := range responses {
		item := resp.Item.(*pb.GetTargetGraphResponse_Targets)
		for _, target := range item.Targets.Targets {
			if target.Id != int32(total) {
				t.Fatalf("target order not preserved at index %d", total)
			}
			total++
		}
	}
	if total != 250 {
		t.Fatalf("total targets = %d, want 250", total)
	}
}

func TestResultToGetTargetGraphResponse_Chunking(t *testing.T) {
	t.Parallel()

	// Create 1500 targets (DefaultTargetChunkSize=1000) → expect 2 chunks + metadata
	numTargets := 1500
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
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	// Expect 2 target chunks + 1 metadata = 3 responses
	if len(responses) != 3 {
		t.Fatalf("got %d responses, want 3", len(responses))
	}

	// Last should be metadata
	if _, ok := responses[2].Item.(*pb.GetTargetGraphResponse_Metadata); !ok {
		t.Fatal("last response should be Metadata")
	}
}
