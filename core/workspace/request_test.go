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

func TestNewRequest_Github_Success(t *testing.T) {
	rawURL := "github://org/repo/pull/123"
	var g git.Interface = nil

	req, err := NewRequest(rawURL, g, "baseRef", "abc123", zap.NewNop().Sugar())
	require.NoError(t, err)
	require.NotNil(t, req)

	gr, ok := req.(*gitRequest)
	require.True(t, ok, "returned Request should be *gitRequest")
	require.Equal(t, "123", gr.requestID)
	require.Equal(t, "abc123", gr.commit)
	require.Nil(t, gr.git)
}

func TestNewRequest_InvalidURL(t *testing.T) {
	rawURL := "://bad"
	var g git.Interface = nil

	req, err := NewRequest(rawURL, g, "baseRef", "", zap.NewNop().Sugar())
	require.Error(t, err)
	require.Nil(t, req)
}
