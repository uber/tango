package workspace

import (
	"context"
	"errors"
	"testing"

	gitmock "github.com/uber/tango/core/git/gitmock"
	requestmock "github.com/uber/tango/core/workspace/requestmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
)

func TestWorkspace_Path(t *testing.T) {
	w := &workspace{path: "/tmp/ws"}
	assert.Equal(t, "/tmp/ws", w.Path())
}

func TestNewWorkspace_SetsFields(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	g := gitmock.NewMockInterface(ctrl)
	tmpDir := t.TempDir()

	w := NewWorkspace(WorkspaceParams{
		Path: tmpDir,
		Git:  g,
	})

	iw, ok := w.(*workspace)
	require.True(t, ok)
	assert.Equal(t, tmpDir, iw.path)
	assert.Equal(t, g, iw.git)
}

func TestWorkspace_ApplyRequests_Success(t *testing.T) {
	w := NewWorkspace(WorkspaceParams{
		Path:   "/tmp/workspace",
		Git:    gitmock.NewMockInterface(gomock.NewController(t)),
		Logger: zap.NewNop().Sugar(),
	})
	ctrl := gomock.NewController(t)
	r1 := requestmock.NewMockRequest(ctrl)
	r2 := requestmock.NewMockRequest(ctrl)
	r1.EXPECT().Apply(gomock.Any()).Return(nil)
	r2.EXPECT().Apply(gomock.Any()).Return(nil)
	err := w.ApplyRequests(context.Background(), []Request{r1, r2})
	require.NoError(t, err)
}

func TestWorkspace_ApplyRequests_StopsOnError(t *testing.T) {
	w := &workspace{}
	ctrl := gomock.NewController(t)
	r1 := requestmock.NewMockRequest(ctrl)
	r2 := requestmock.NewMockRequest(ctrl)
	r1.EXPECT().Apply(gomock.Any()).Return(errors.New("apply failed"))

	err := w.ApplyRequests(context.Background(), []Request{r1, r2})
	require.Error(t, err)
}

func TestWorkspace_Checkout_RevParseSuccess(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	g := gitmock.NewMockInterface(ctrl)
	wsPath := "/tmp/workspace"
	w := &workspace{path: wsPath, git: g, logger: zap.NewNop().Sugar()}

	ref := "abc123"
	commitRef := ref + "^{commit}"
	g.EXPECT().RevParse(gomock.Any(), commitRef).Return("abc123", nil)
	// expect Checkout with commit hash and workspace path as option
	g.EXPECT().Checkout(gomock.Any(), "abc123").Return(nil)

	err := w.Checkout(context.Background(), "origin", ref)
	require.NoError(t, err)
}

func TestWorkspace_Checkout_FetchError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	g := gitmock.NewMockInterface(ctrl)
	wsPath := "/tmp/workspace"
	w := &workspace{path: wsPath, git: g, logger: zap.NewNop().Sugar()}

	remote := "origin"
	ref := "refs/heads/feature"
	commitRef := ref + "^{commit}"

	g.EXPECT().RevParse(gomock.Any(), commitRef).Return("", errors.New("missing"))
	g.EXPECT().Fetch(gomock.Any(), remote, ref).Return(errors.New("fetch failed"))

	err := w.Checkout(context.Background(), remote, ref)
	require.Error(t, err)
}

func TestWorkspace_Release(t *testing.T) {
	w := &workspace{}
	err := w.Release()
	require.NoError(t, err)
}
