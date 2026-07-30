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

package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"pgregory.net/rapid"
)

type runnerCall struct {
	kind  string // "run" | "output" | "runWithStdin"
	dir   string
	name  string
	args  []string
	stdin []byte
}

type mockRunner struct {
	calls []runnerCall
	out   []byte
	err   error
	// returnCtxErr, if true, makes run/output/runWithStdin return ctx.Err()
	// instead of err — simulating what an exec-based runner returns when
	// its command is killed by a canceled/expired context.
	returnCtxErr bool
}

func (m *mockRunner) run(ctx context.Context, dir string, name string, args ...string) error {
	m.calls = append(m.calls, runnerCall{kind: "run", dir: dir, name: name, args: append([]string(nil), args...)})
	if m.returnCtxErr {
		return ctx.Err()
	}
	return m.err
}
func (m *mockRunner) output(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, runnerCall{kind: "output", dir: dir, name: name, args: append([]string(nil), args...)})
	if m.returnCtxErr {
		return nil, ctx.Err()
	}
	return append([]byte(nil), m.out...), m.err
}
func (m *mockRunner) runWithStdin(ctx context.Context, dir string, name string, stdin []byte, args ...string) error {
	m.calls = append(m.calls, runnerCall{kind: "runWithStdin", dir: dir, name: name, args: append([]string(nil), args...), stdin: append([]byte(nil), stdin...)})
	if m.returnCtxErr {
		return ctx.Err()
	}
	return m.err
}

func TestCheckout_wrapsErrTimeoutWhenTimeoutElapses(t *testing.T) {
	// A parent deadline already in the past causes the derived timeout
	// context to be immediately Done with context.DeadlineExceeded, before
	// the runner is even invoked.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()
	m := &mockRunner{returnCtxErr: true}
	g := &impl{directory: "/repo", runner: m}
	err := g.Checkout(ctx, "feature")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTimeout)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestCheckout_doesNotWrapErrTimeoutOnParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := &mockRunner{returnCtxErr: true}
	g := &impl{directory: "/repo", runner: m}
	err := g.Checkout(ctx, "feature")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrTimeout)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestWrapError_marksFatalExitCodesAsErrFatal(t *testing.T) {
	tests := []struct {
		name      string
		exitCode  int
		wantFatal bool
	}{
		{name: "fatal error exit code", exitCode: 128, wantFatal: true},
		{name: "usage error exit code", exitCode: 129, wantFatal: true},
		{name: "generic/conditional exit code", exitCode: 1, wantFatal: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runErr := exec.Command("sh", "-c", fmt.Sprintf("exit %d", tt.exitCode)).Run()
			require.Error(t, runErr)

			err := wrapError(context.Background(), []string{"checkout", "feature"}, runErr)
			require.Error(t, err)
			if tt.wantFatal {
				assert.ErrorIs(t, err, ErrFatal)
			} else {
				assert.NotErrorIs(t, err, ErrFatal)
			}
		})
	}
}

func TestClone_usesRunnerWithDirAndArgs(t *testing.T) {
	m := &mockRunner{}
	g := &impl{directory: "/repo", runner: m}
	require.NoError(t, g.Clone(context.Background(), "target", "/dest", "--depth=1"))
	require.Len(t, m.calls, 1)
	c := m.calls[0]
	require.Equal(t, "run", c.kind)
	require.Equal(t, "/repo", c.dir)
	require.Equal(t, "git", c.name)
	assert.EqualValues(t, []string{"clone", "--depth=1", "target", "/dest"}, c.args)
}

func TestCheckout_usesDashCAndRunner(t *testing.T) {
	m := &mockRunner{}
	g := &impl{directory: "/repo", runner: m}
	require.NoError(t, g.Checkout(context.Background(), "feature", "--force"))
	require.Len(t, m.calls, 1)
	c := m.calls[0]
	assert.EqualValues(t, []string{"checkout", "feature", "--force"}, c.args)
}

func TestFetch_callsRunnerWithDir(t *testing.T) {
	m := &mockRunner{}
	g := &impl{directory: "/repo", runner: m}
	require.NoError(t, g.Fetch(context.Background(), "origin", "refs/heads/main"))
	require.Len(t, m.calls, 1)
	c := m.calls[0]
	require.Equal(t, "run", c.kind)
	require.Equal(t, "/repo", c.dir)
	require.True(t, reflect.DeepEqual([]string{"fetch", "origin", "refs/heads/main"}, c.args))
}

func TestDiff_returnsRunnerOutput(t *testing.T) {
	m := &mockRunner{out: []byte("diff-output")}
	g := &impl{directory: "/repo", runner: m}
	out, err := g.Diff(context.Background(), "base", "head", "--name-only")
	require.NoError(t, err)
	require.Equal(t, []byte("diff-output"), out)
	require.Len(t, m.calls, 1)
	c := m.calls[0]
	require.Equal(t, "output", c.kind)
	require.Equal(t, "/repo", c.dir)
	require.True(t, reflect.DeepEqual([]string{"diff", "base", "head", "--name-only"}, c.args))
}

func TestApplyPatch_passesPatchViaStdin(t *testing.T) {
	m := &mockRunner{}
	g := &impl{directory: "/repo", runner: m}
	patch := []byte("fake-patch")
	require.NoError(t, g.ApplyPatch(context.Background(), patch))
	require.Len(t, m.calls, 1)
	c := m.calls[0]
	require.Equal(t, "runWithStdin", c.kind)
	require.Equal(t, "/repo", c.dir)
	require.Equal(t, "git", c.name)
	require.True(t, reflect.DeepEqual([]string{"apply", "--3way", "--whitespace", "nowarn", "--index", "-"}, c.args))
	require.True(t, reflect.DeepEqual(patch, c.stdin))
}

func TestRevParse_returnsStringFromRunner(t *testing.T) {
	m := &mockRunner{out: []byte("abc123\n")}
	g := &impl{directory: "/repo", runner: m}
	got, err := g.RevParse(context.Background(), "HEAD")
	require.NoError(t, err)
	require.Equal(t, "abc123", got)
	require.Len(t, m.calls, 1)
	c := m.calls[0]
	require.Equal(t, "output", c.kind)
	require.Equal(t, "/repo", c.dir)
	require.True(t, reflect.DeepEqual([]string{"rev-parse", "HEAD"}, c.args))
}

func TestCommit_usesRunnerWithDirAndArgs(t *testing.T) {
	m := &mockRunner{}
	g := &impl{directory: "/repo", runner: m}
	require.NoError(t, g.Commit(context.Background(), "commit message"))
	require.Len(t, m.calls, 1)
	c := m.calls[0]
	require.Equal(t, "run", c.kind)
	require.Equal(t, "/repo", c.dir)
	require.Equal(t, "git", c.name)
	assert.EqualValues(t, []string{"commit", "-am", "commit message"}, c.args)
}

func TestSubmoduleUpdate_usesRunnerWithDirAndArgs(t *testing.T) {
	m := &mockRunner{}
	g := &impl{directory: "/repo", runner: m}
	require.NoError(t, g.SubmoduleUpdate(context.Background()))
	require.Len(t, m.calls, 1)
	c := m.calls[0]
	require.Equal(t, "run", c.kind)
	require.Equal(t, "/repo", c.dir)
	require.Equal(t, "git", c.name)
	assert.EqualValues(t, []string{"submodule", "update", "--init", "--recursive"}, c.args)
}

func TestDiffWithStatus_parsesNameStatusOutput(t *testing.T) {
	tests := []struct {
		name    string
		output  []byte
		want    []DiffEntry
		wantErr bool
	}{
		{
			name:   "modified and added files",
			output: []byte("M\x00src/foo.go\x00A\x00src/bar.go\x00"),
			want: []DiffEntry{
				{Status: "M", Path: "src/foo.go"},
				{Status: "A", Path: "src/bar.go"},
			},
		},
		{
			name:   "rename uses destination path",
			output: []byte("R100\x00old/path.go\x00new/path.go\x00"),
			want:   []DiffEntry{{Status: "R", Path: "new/path.go"}},
		},
		{
			name:   "empty diff returns nil",
			output: []byte(""),
			want:   nil,
		},
		{
			name:    "runner error propagates",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var runErr error
			if tt.wantErr {
				runErr = errors.New("git error")
			}
			m := &mockRunner{out: tt.output, err: runErr}
			g := &impl{directory: "/repo", runner: m}
			got, err := g.DiffWithStatus(context.Background(), "base", "head")
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetCommitTimeSecond_parsesUnixTimestamp(t *testing.T) {
	m := &mockRunner{out: []byte("1700000000\n")}
	g := &impl{directory: "/repo", runner: m}
	got, err := g.GetCommitTimeSecond(context.Background(), "HEAD")
	require.NoError(t, err)
	assert.Equal(t, int64(1700000000), got)
	require.Len(t, m.calls, 1)
	assert.EqualValues(t, []string{"log", "-1", "--format=%ct", "HEAD"}, m.calls[0].args)
}

func TestGetCommitTimeSecond_errorPropagates(t *testing.T) {
	m := &mockRunner{err: assert.AnError}
	g := &impl{directory: "/repo", runner: m}
	_, err := g.GetCommitTimeSecond(context.Background(), "HEAD")
	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError)
}

func TestDefaultGit_FileHashes(t *testing.T) {
	tests := []struct {
		name       string
		giveOutput []byte
		wantHashes map[string][]byte
		wantError  error
	}{
		{
			name: "happy case",
			giveOutput: []byte(
				"100644 blob d236\tfile1\x00100644 blob 9bcc\tfile2\x00"),
			wantHashes: map[string][]byte{
				"file1": []byte{0xd2, 0x36},
				"file2": []byte{0x9b, 0xcc},
			},
		},
		{
			name:       "ignore bad format",
			giveOutput: []byte("100644 blob d236 file1\x00"),
			wantHashes: map[string][]byte{},
		},
		{
			name:       "ignore bad hex",
			giveOutput: []byte("100644 blob not_a_hex\tfile1\x00"),
			wantHashes: map[string][]byte{},
		},
		{
			name:      "git error",
			wantError: assert.AnError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			m := &mockRunner{}
			g := &impl{
				directory: "/repo",
				runner:    m,
				logger:    zap.NewNop().Sugar(),
			}
			m.out = tt.giveOutput
			m.err = tt.wantError
			gotHashes, err := g.FileHashes(ctx, tt.name)
			if tt.wantError != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantError)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantHashes, gotHashes)
		})
	}
}

func TestGitPathnames_roundTripAcrossRealGitBoundary(t *testing.T) {
	paths := []string{
		"file .txt",
		"file\tname.txt",
		"file\"name.txt",
		"file\\name.txt",
		"nested/é雪.txt",
	}
	repo := t.TempDir()

	runGit(t, repo, "init", "--quiet")
	runGit(t, repo, "config", "user.email", "tango-test@example.com")
	runGit(t, repo, "config", "user.name", "Tango Test")
	runGit(t, repo, "commit", "--quiet", "--allow-empty", "-m", "base")
	for _, path := range paths {
		absolutePath := filepath.Join(repo, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(absolutePath), 0o755))
		require.NoError(t, os.WriteFile(absolutePath, []byte(path), 0o644))
	}
	runGit(t, repo, "add", "--all")
	runGit(t, repo, "commit", "--quiet", "-m", "paths")

	client := New(repo, zap.NewNop().Sugar())
	diff, err := client.DiffWithStatus(t.Context(), "HEAD^", "HEAD")
	require.NoError(t, err)
	diffPaths := make(map[string]string, len(diff))
	for _, entry := range diff {
		diffPaths[entry.Path] = entry.Status
	}

	hashes, err := client.FileHashes(t.Context(), "HEAD")
	require.NoError(t, err)
	hashPaths := make(map[string]struct{}, len(hashes))
	for path := range hashes {
		hashPaths[path] = struct{}{}
	}

	wantDiffPaths := make(map[string]string, len(paths))
	wantHashPaths := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		wantDiffPaths[path] = "A"
		wantHashPaths[path] = struct{}{}
	}
	require.Equal(t, wantDiffPaths, diffPaths, "DiffWithStatus must preserve exact paths")
	require.Equal(t, wantHashPaths, hashPaths, "FileHashes must preserve exact paths")
}

func TestDiffWithStatus_preservesGeneratedPathnames(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		paths := rapid.SliceOfNDistinct(
			legalGitPath(),
			1,
			4,
			func(path string) string { return path },
		).Draw(t, "paths")

		var output bytes.Buffer
		for _, path := range paths {
			output.WriteString("A")
			output.WriteByte(0)
			output.WriteString(path)
			output.WriteByte(0)
		}
		client := &impl{directory: "/repo", runner: &mockRunner{out: output.Bytes()}}
		diff, err := client.DiffWithStatus(t.Context(), "base", "head")
		require.NoError(t, err)
		diffPaths := make(map[string]string, len(diff))
		for _, entry := range diff {
			diffPaths[entry.Path] = entry.Status
		}

		wantDiffPaths := make(map[string]string, len(paths))
		for _, path := range paths {
			wantDiffPaths[path] = "A"
		}
		require.Equal(t, wantDiffPaths, diffPaths, "DiffWithStatus must preserve exact paths")
	})
}

func legalGitPath() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		depth := rapid.SampledFrom([]int{0, 1, 1, 2, 2}).Draw(t, "depth")
		components := make([]string, 0, depth+1)
		for i := range depth {
			components = append(components, "dir"+legalGitPathToken(t, "directory", i))
		}
		components = append(components, "file"+legalGitPathToken(t, "file", depth)+".txt")
		return filepath.ToSlash(filepath.Join(components...))
	})
}

func legalGitPathToken(t *rapid.T, kind string, index int) string {
	return rapid.SampledFrom([]string{
		" ", " ", "\t", "\"", "'", "\\", "é", "雪",
	}).Draw(t, kind+string(rune('0'+index)))
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, output)
}

func TestFatalExitCode_wrapsErrFatal(t *testing.T) {
	// A fatal exit (128) from any of the previously-unwrapped methods must
	// surface as errors.Is(err, ErrFatal) so the orchestrator's
	// classifyGitError can tag it as an infra failure.
	fatalErr := exec.Command("sh", "-c", "exit 128").Run()
	require.Error(t, fatalErr)

	tests := []struct {
		name string
		call func(g *impl) error
	}{
		{
			name: "RevParse",
			call: func(g *impl) error {
				_, err := g.RevParse(context.Background(), "HEAD")
				return err
			},
		},
		{
			name: "IsAncestor",
			call: func(g *impl) error {
				_, err := g.IsAncestor(context.Background(), "a", "b")
				return err
			},
		},
		{
			name: "GetCommitTimeSecond",
			call: func(g *impl) error {
				_, err := g.GetCommitTimeSecond(context.Background(), "HEAD")
				return err
			},
		},
		{
			name: "FileHashes",
			call: func(g *impl) error {
				_, err := g.FileHashes(context.Background(), "HEAD")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &mockRunner{err: fatalErr}
			g := &impl{directory: "/repo", runner: m, logger: zap.NewNop().Sugar()}
			err := tt.call(g)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrFatal, "fatal exit code must wrap ErrFatal")
		})
	}
}
