package targethasher

import (
	"os"
	"path/filepath"
	"testing"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TODO: Add tests to check for bazel query for proto.bin output.
func TestNewSourceHasher_buildsMaps(t *testing.T) {
	h := NewSourceHasher(Params{
		WorkspaceRoot: "/ws",
		HashConfig: HashConfig{
			KnownSourceHashes: map[string][]byte{"a/b.txt": []byte("abc")},
		},
	})
	dh, ok := h.(*diskHashHelper)
	assert.True(t, ok, "expected *diskHashHelper, got %T", h)
	assert.Equal(t, "/ws", dh.workspaceroot, "workspaceroot mismatch")
	assert.Equal(t, "abc", string(dh.knownFileHashes["a/b.txt"]), "knownFileHashes mismatch")
}

func TestNoOpHasher_returnsNil(t *testing.T) {
	h := &noOpHasher{}
	sf := &buildpb.SourceFile{Name: strPtr("//:dummy")}
	got, err := h.HashSourceFile(sf)
	assert.NoError(t, err, "unexpected error: %v", err)
	assert.Nil(t, got, "expected nil hash, got %v", got)
}

func TestDiskHashHelper_KnownFileHashUsed(t *testing.T) {
	tmp := t.TempDir()
	wsRoot := tmp
	rel := filepath.Join("pkg", "file.txt")
	abs := filepath.Join(wsRoot, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		require.NoError(t, err, "mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte("contents"), 0o644); err != nil {
		require.NoError(t, err, "write: %v", err)
	}

	known := []byte("KNOWN")
	h := &diskHashHelper{
		workspaceroot:   wsRoot,
		knownFileHashes: map[string][]byte{filepath.ToSlash(rel): known},
	}
	sf := &buildpb.SourceFile{
		Name:            strPtr("//" + filepath.ToSlash(filepath.Dir(rel)) + ":" + filepath.Base(rel)),
		Location:        strPtr(abs + ":1:1"),
		VisibilityLabel: []string{"//visibility:private"},
	}
	got, err := h.HashSourceFile(sf)
	assert.NoError(t, err, "unexpected err: %v", err)
	assert.Equal(t, string(known), string(got), "expected known hash %q, got %q", known, got)
}

func TestDiskHashHelper_NonDefaultVisibilityForcesDiskHash(t *testing.T) {
	tmp := t.TempDir()
	wsRoot := tmp
	rel := filepath.Join("pkg", "file.txt")
	abs := filepath.Join(wsRoot, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		require.NoError(t, err, "mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte("X"), 0o644); err != nil {
		require.NoError(t, err, "write: %v", err)
	}

	h := &diskHashHelper{
		workspaceroot:   wsRoot,
		knownFileHashes: map[string][]byte{filepath.ToSlash(rel): []byte("KNOWN")},
	}
	sf := &buildpb.SourceFile{
		Name:            strPtr("//" + filepath.ToSlash(filepath.Dir(rel)) + ":" + filepath.Base(rel)),
		Location:        strPtr(abs + ":1:1"),
		VisibilityLabel: []string{"//visibility:public"}, // non-default
	}
	got, err := h.HashSourceFile(sf)
	assert.NoError(t, err, "unexpected err: %v", err)
	assert.NotEqual(t, string(got), "KNOWN", "expected disk hash, but got known hash")
	assert.NotEqual(t, []byte{}, got, "expected non-empty disk hash")
}

func TestDiskHashHelper_HashesFileFromDisk(t *testing.T) {
	tmp := t.TempDir()
	wsRoot := tmp
	rel := filepath.Join("pkg", "file.txt")
	abs := filepath.Join(wsRoot, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		require.NoError(t, err, "mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte("hello world"), 0o644); err != nil {
		require.NoError(t, err, "write: %v", err)
	}

	h := &diskHashHelper{
		workspaceroot:   wsRoot,
		knownFileHashes: map[string][]byte{},
	}
	sf := &buildpb.SourceFile{
		Name:            strPtr("//" + filepath.ToSlash(filepath.Dir(rel)) + ":" + filepath.Base(rel)),
		Location:        strPtr(abs),
		VisibilityLabel: []string{"//visibility:private"},
	}
	got, err := h.HashSourceFile(sf)
	assert.NoError(t, err, "unexpected err: %v", err)
	assert.NotEmpty(t, got, "expected non-empty hash from disk")
}

func TestDiskHashHelper_HashesDirectory(t *testing.T) {
	tmp := t.TempDir()
	wsRoot := tmp
	dirRel := "mydir"
	dirAbs := filepath.Join(wsRoot, dirRel)
	if err := os.MkdirAll(dirAbs, 0o755); err != nil {
		require.NoError(t, err, "mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirAbs, "a.txt"), []byte("aaa"), 0o644); err != nil {
		require.NoError(t, err, "write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirAbs, "b.txt"), []byte("bbb"), 0o644); err != nil {
		require.NoError(t, err, "write b.txt: %v", err)
	}

	h := &diskHashHelper{
		workspaceroot:   wsRoot,
		knownFileHashes: map[string][]byte{},
	}
	sf := &buildpb.SourceFile{
		Name:            strPtr("//:" + dirRel),
		Location:        strPtr(dirAbs),
		VisibilityLabel: []string{"//visibility:private"},
	}
	got, err := h.HashSourceFile(sf)
	assert.NoError(t, err, "unexpected err: %v", err)
	assert.NotEmpty(t, got, "expected non-empty hash for directory")
}

func strPtr(s string) *string { return &s }
