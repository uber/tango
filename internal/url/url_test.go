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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/uber/tango/entity"
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
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ToShortRemote(tt.remote))
		})
	}
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
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, GetReqURLsHash(tt.in))
		})
	}
}
