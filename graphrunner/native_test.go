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

package graphrunner

import (
	"context"
	"testing"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/bazel"
	"github.com/uber/tango/core/bazel/bazelmock"
	gitmock "github.com/uber/tango/core/git/gitmock"
	"github.com/uber/tango/core/workspace"
	"go.uber.org/mock/gomock"
)

func TestCompute_CallsBazelAndReturnsResult(t *testing.T) {
	ctrl := gomock.NewController(t)
	bazelMock := bazelmock.NewMockBazel(ctrl)
	gitMock := gitmock.NewMockInterface(ctrl)
	gitMock.EXPECT().FileHashes(gomock.Any(), gomock.Any()).Return(map[string][]byte{}, nil)
	ruleName := "//:a"
	ruleClass := "go_library"
	bazelMock.EXPECT().ExecuteQuery(gomock.Any(), gomock.Any()).Return(&bazel.QueryResponse{Result: &buildpb.QueryResult{Target: []*buildpb.Target{
		{
			Type: buildpb.Target_RULE.Enum(),
			Rule: &buildpb.Rule{
				Name:      &ruleName,
				RuleClass: &ruleClass,
			},
		},
	}}}, nil)
	gr := NewNativeGraphRunner(NativeGraphRunnerParams{
		BazelClient: bazelMock,
		GitClient:   gitMock,
		// leave HashConfig zero; not asserted here
	})
	ws := workspace.NewWorkspace(workspace.WorkspaceParams{
		Path: "/tmp/ws",
	})

	res, err := gr.Compute(context.Background(), ws)
	// get target hash
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 1, len(res.Targets))
	assert.Equal(t, ruleName, res.Targets[ruleName].Name)
	assert.Equal(t, ruleClass, res.Targets[ruleName].Rule.GetRuleClass())
}

func TestCompute_PropagatesError(t *testing.T) {
	ctrl := gomock.NewController(t)
	bazelMock := bazelmock.NewMockBazel(ctrl)
	bazelMock.EXPECT().ExecuteQuery(gomock.Any(), gomock.Any()).Return(nil, assert.AnError)
	gr := NewNativeGraphRunner(NativeGraphRunnerParams{BazelClient: bazelMock})
	ws := workspace.NewWorkspace(workspace.WorkspaceParams{
		Path: "/tmp/ws",
	})

	res, err := gr.Compute(context.Background(), ws)
	require.Error(t, err)
	// Expect an empty result on error
	assert.NotNil(t, res.Targets)
	assert.Zero(t, len(res.Targets))
}
