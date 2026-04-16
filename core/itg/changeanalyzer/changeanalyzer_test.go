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

package changeanalyzer

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/git"
	"github.com/uber/tango/core/git/gitmock"
	"go.uber.org/mock/gomock"
)

func newAnalyzer(t *testing.T, g git.Interface) Analyzer {
	t.Helper()
	a, err := NewAnalyzer(g, Config{
		BuildFilePatterns:    []string{`(^|/)BUILD(\.bazel)?$`},
		CriticalFilePatterns: []string{`(^|/)WORKSPACE(\.bazel)?$`},
		IgnoredFilePatterns:  []string{`(^|/)METADATA$`},
	})
	require.NoError(t, err)
	return a
}

func TestGetFileCategory(t *testing.T) {
	a := newAnalyzer(t, nil)
	tests := []struct {
		path string
		want FileCategory
	}{
		{"foo/BUILD", BuildFile},
		{"foo/BUILD.bazel", BuildFile},
		{"WORKSPACE", CriticalFile},
		{"WORKSPACE.bazel", CriticalFile},
		{"foo/METADATA", IgnoredFile},
		{"foo/bar.go", RegularFile},
		{"foo/bar.bzl", ExtensionFile},
		{"foo/bar.py", RegularFile},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.want, a.GetFileCategory(tt.path))
		})
	}
}

func TestNewAnalyzer_invalidPattern(t *testing.T) {
	_, err := NewAnalyzer(nil, Config{BuildFilePatterns: []string{"[invalid"}})
	require.Error(t, err)
}

func TestAnalyzeChange_noChanges(t *testing.T) {
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)
	g.EXPECT().DiffWithStatus(gomock.Any(), "base", "head").Return(nil, nil)

	a := newAnalyzer(t, g)
	resp, err := a.AnalyzeChange(context.Background(), &AnalyzeChangeRequest{BaseRef: "base", TargetRef: "head"})
	require.NoError(t, err)
	assert.Equal(t, NoChange, resp.ChangeComplexity)
}

func TestAnalyzeChange_regularFilesOnly(t *testing.T) {
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)
	g.EXPECT().DiffWithStatus(gomock.Any(), "base", "head").Return([]git.DiffEntry{
		{Status: "M", Path: "src/foo.go"},
		{Status: "M", Path: "src/bar.go"},
	}, nil)

	a := newAnalyzer(t, g)
	resp, err := a.AnalyzeChange(context.Background(), &AnalyzeChangeRequest{BaseRef: "base", TargetRef: "head"})
	require.NoError(t, err)
	assert.Equal(t, RegularFilesModificationOnly, resp.ChangeComplexity)
}

func TestAnalyzeChange_buildFileChange(t *testing.T) {
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)
	g.EXPECT().DiffWithStatus(gomock.Any(), "base", "head").Return([]git.DiffEntry{
		{Status: "M", Path: "src/foo.go"},
		{Status: "M", Path: "src/BUILD"},
	}, nil)

	a := newAnalyzer(t, g)
	resp, err := a.AnalyzeChange(context.Background(), &AnalyzeChangeRequest{BaseRef: "base", TargetRef: "head"})
	require.NoError(t, err)
	assert.Equal(t, ReparsePackagesNeeded, resp.ChangeComplexity)
}

func TestAnalyzeChange_criticalFile(t *testing.T) {
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)
	g.EXPECT().DiffWithStatus(gomock.Any(), "base", "head").Return([]git.DiffEntry{
		{Status: "M", Path: "WORKSPACE"},
	}, nil)

	a := newAnalyzer(t, g)
	resp, err := a.AnalyzeChange(context.Background(), &AnalyzeChangeRequest{BaseRef: "base", TargetRef: "head"})
	require.NoError(t, err)
	assert.Equal(t, FullCalculationRequired, resp.ChangeComplexity)
}

func TestAnalyzeChange_ignoredFilesOnly(t *testing.T) {
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)
	g.EXPECT().DiffWithStatus(gomock.Any(), "base", "head").Return([]git.DiffEntry{
		{Status: "M", Path: "foo/METADATA"},
	}, nil)

	a := newAnalyzer(t, g)
	resp, err := a.AnalyzeChange(context.Background(), &AnalyzeChangeRequest{BaseRef: "base", TargetRef: "head"})
	require.NoError(t, err)
	assert.Equal(t, NoChange, resp.ChangeComplexity)
}

func TestAnalyzeChange_extensionFile(t *testing.T) {
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)
	g.EXPECT().DiffWithStatus(gomock.Any(), "base", "head").Return([]git.DiffEntry{
		{Status: "M", Path: "rules/foo.bzl"},
	}, nil)

	a := newAnalyzer(t, g)
	resp, err := a.AnalyzeChange(context.Background(), &AnalyzeChangeRequest{BaseRef: "base", TargetRef: "head"})
	require.NoError(t, err)
	// .bzl is neither regular, build, nor ignored — not ReparsePackagesNeeded either
	assert.Equal(t, UnknownComplexity, resp.ChangeComplexity)
}

func TestAnalyzeChange_fetchRetryOnExitError(t *testing.T) {
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)
	exitErr := &exec.ExitError{}
	g.EXPECT().DiffWithStatus(gomock.Any(), "base", "head").Return(nil, exitErr).Times(1)
	g.EXPECT().Fetch(gomock.Any(), "origin", "main", "--no-tags").Return(nil)
	g.EXPECT().DiffWithStatus(gomock.Any(), "base", "head").Return([]git.DiffEntry{
		{Status: "M", Path: "src/foo.go"},
	}, nil)

	a := newAnalyzer(t, g)
	resp, err := a.AnalyzeChange(context.Background(), &AnalyzeChangeRequest{BaseRef: "base", TargetRef: "head"})
	require.NoError(t, err)
	assert.Equal(t, RegularFilesModificationOnly, resp.ChangeComplexity)
}

func TestAnalyzeChange_nonExitErrorNoRetry(t *testing.T) {
	ctrl := gomock.NewController(t)
	g := gitmock.NewMockInterface(ctrl)
	g.EXPECT().DiffWithStatus(gomock.Any(), "base", "head").Return(nil, errors.New("network error"))

	a := newAnalyzer(t, g)
	_, err := a.AnalyzeChange(context.Background(), &AnalyzeChangeRequest{BaseRef: "base", TargetRef: "head"})
	require.Error(t, err)
}
