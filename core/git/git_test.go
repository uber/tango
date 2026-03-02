package git

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
}

func (m *mockRunner) run(_ context.Context, dir string, name string, args ...string) error {
	m.calls = append(m.calls, runnerCall{kind: "run", dir: dir, name: name, args: append([]string(nil), args...)})
	return m.err
}
func (m *mockRunner) output(_ context.Context, dir string, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, runnerCall{kind: "output", dir: dir, name: name, args: append([]string(nil), args...)})
	return append([]byte(nil), m.out...), m.err
}
func (m *mockRunner) runWithStdin(_ context.Context, dir string, name string, stdin []byte, args ...string) error {
	m.calls = append(m.calls, runnerCall{kind: "runWithStdin", dir: dir, name: name, args: append([]string(nil), args...), stdin: append([]byte(nil), stdin...)})
	return m.err
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
	require.Equal(t, "abc123\n", got)
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
				`100644 blob d236	file1
100644 blob 9bcc	file2`),
			wantHashes: map[string][]byte{
				"file1": []byte{0xd2, 0x36},
				"file2": []byte{0x9b, 0xcc},
			},
		},
		{
			name:       "ignore bad format",
			giveOutput: []byte("100644 blob d236 file1"),
			wantHashes: map[string][]byte{},
		},
		{
			name:       "ignore bad hex",
			giveOutput: []byte("100644 blob not_a_hex	file1"),
			wantHashes: map[string][]byte{},
		},
		{
			name:      "git error",
			wantError: errors.New(""),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			m := &mockRunner{}
			g := &impl{
				directory: "/repo",
				runner:    m,
			}
			m.out = tt.giveOutput
			m.err = tt.wantError
			gotHashes, err := g.FileHashes(ctx, tt.name)
			require.Equal(t, tt.wantError, err)
			assert.Equal(t, tt.wantHashes, gotHashes)
		})
	}
}
