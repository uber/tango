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
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/config"
	"github.com/uber/tango/core/bazel"
	"github.com/uber/tango/core/bazel/bazelmock"
	gitmock "github.com/uber/tango/core/git/gitmock"
	"github.com/uber/tango/core/workspace"
	"go.uber.org/mock/gomock"
)

func boolPtr(b bool) *bool { return &b }

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
		Config:      config.RepositoryConfig{BzlmodEnabled: boolPtr(false)},
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

func TestCompute_BzlmodCollapsesExternalTargets(t *testing.T) {
	// Set up a fake output base with marker files so the bzlmod
	// collapse path can read them without a real bazel installation.
	outputBase := t.TempDir()
	markerDir := filepath.Join(outputBase, "external")
	require.NoError(t, os.MkdirAll(markerDir, 0o755))

	markerHash := hex.EncodeToString([]byte("fake-hash-for-test"))
	require.NoError(t, os.WriteFile(
		filepath.Join(markerDir, "@myrepo.marker"),
		[]byte(markerHash+"\n"),
		0o644,
	))

	// Create a fake bazel script that prints the output base.
	fakeBazel := filepath.Join(t.TempDir(), "fake-bazel")
	require.NoError(t, os.WriteFile(fakeBazel, []byte("#!/bin/sh\necho "+outputBase+"\n"), 0o755))

	ctrl := gomock.NewController(t)
	bazelMock := bazelmock.NewMockBazel(ctrl)
	gitMock := gitmock.NewMockInterface(ctrl)
	gitMock.EXPECT().FileHashes(gomock.Any(), gomock.Any()).Return(map[string][]byte{}, nil)

	srcName := "@@myrepo//pkg:file.go"
	srcType := "source file"
	ruleName := "//:a"
	ruleClass := "go_library"
	bazelMock.EXPECT().ExecuteQuery(gomock.Any(), gomock.Any()).Return(&bazel.QueryResponse{Result: &buildpb.QueryResult{Target: []*buildpb.Target{
		{
			Type: buildpb.Target_SOURCE_FILE.Enum(),
			SourceFile: &buildpb.SourceFile{
				Name: &srcName,
			},
		},
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
		Config: config.RepositoryConfig{
			BzlmodEnabled:    boolPtr(true),
			BazelCommandPath: fakeBazel,
		},
	})
	ws := workspace.NewWorkspace(workspace.WorkspaceParams{
		Path: t.TempDir(),
	})

	res, err := gr.Compute(context.Background(), ws)
	require.NoError(t, err)
	require.NotNil(t, res)

	// The external source file should have been collapsed with a hash
	// derived from the marker file, not left nil.
	extTarget, ok := res.Targets[srcName]
	require.True(t, ok, "external target %q should be in results", srcName)
	assert.NotNil(t, extTarget.Hash, "collapsed external target should have a hash")
	assert.Equal(t, srcType, extTarget.RuleType)

	// The internal rule target should also be present.
	_, ok = res.Targets[ruleName]
	assert.True(t, ok, "internal target %q should be in results", ruleName)
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
