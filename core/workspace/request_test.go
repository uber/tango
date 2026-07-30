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

package workspace

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/git"
	"go.uber.org/zap"
)

const _testHeadSHA = "c3a4b5d6e7f80912a3b4c5d6e7f80912a3b4c5d6"

func TestNewRequest_Github_Success(t *testing.T) {
	rawURL := "github://github.com/org/repo/pull/123/" + _testHeadSHA
	var g git.Interface = nil

	req, err := NewRequest(rawURL, g, "baseRef", zap.NewNop().Sugar())
	require.NoError(t, err)
	require.NotNil(t, req)
	gr, ok := req.(*gitRequest)
	require.True(t, ok, "returned Request should be *gitRequest")
	require.Equal(t, "123", gr.requestID)
	require.Equal(t, _testHeadSHA, gr.headSHA)
	require.Nil(t, gr.git)
}

func TestNewRequest_InvalidURL(t *testing.T) {
	rawURL := "://bad"
	var g git.Interface = nil

	req, err := NewRequest(rawURL, g, "baseRef", zap.NewNop().Sugar())
	require.Error(t, err)
	require.Nil(t, req)
}

func TestNewRequest_InvalidScheme(t *testing.T) {
	rawURL := "phabricator://bad"
	var g git.Interface = nil

	req, err := NewRequest(rawURL, g, "baseRef", zap.NewNop().Sugar())
	require.Error(t, err)
	require.Nil(t, req)
}

func TestNewRequest_NonCanonicalURI(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "legacy hostless form", url: "github://org/repo/pull/123"},
		{name: "missing head sha", url: "github://github.com/org/repo/pull/123"},
		{name: "uppercase host", url: "github://GitHub.com/org/repo/pull/123/" + _testHeadSHA},
		{name: "short sha", url: "github://github.com/org/repo/pull/123/abc123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := NewRequest(tt.url, nil, "baseRef", zap.NewNop().Sugar())
			require.Error(t, err)
			require.Nil(t, req)
		})
	}
}
