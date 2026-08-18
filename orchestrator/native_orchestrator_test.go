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

package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"encoding/gob"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/config"
	"github.com/uber/tango/core/cachekey"
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/core/git"
	gitmock "github.com/uber/tango/core/git/gitmock"
	"github.com/uber/tango/core/repomanager"
	repomanagermock "github.com/uber/tango/core/repomanager/mock"
	"github.com/uber/tango/core/storage"
	storagemock "github.com/uber/tango/core/storage/storagemock"
	targethasher "github.com/uber/tango/core/targethasher"
	workspacemock "github.com/uber/tango/core/workspace/workspacemock"
	"github.com/uber/tango/entity"
	graphmock "github.com/uber/tango/graphrunner/mock"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap/zaptest"
)

func TestNative_GetTargetGraph_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	st := storagemock.NewMockStorage(ctrl)
	var buf bytes.Buffer
	require.NoError(t, gob.NewEncoder(&buf).Encode(entity.GetTargetGraphResponse{Targets: []entity.OptimizedTarget{{ID: 1}}}))
	// Single fetch by remote/treehash for the graph
	st.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{
		ReadCloser: io.NopCloser(bytes.NewReader(buf.Bytes())),
	}, nil)

	// Inject git and workspace
	g := gitmock.NewMockInterface(ctrl)
	g.EXPECT().RevParse(gomock.Any(), "HEAD^{tree}").Return("raw-treehash", nil)
	ws := workspacemock.NewMockWorkspace(ctrl)
	ws.EXPECT().Path().Return("/tmp/ws")
	ws.EXPECT().Checkout(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	ws.EXPECT().ApplyRequests(gomock.Any(), gomock.Any()).Return(nil)
	ws.EXPECT().Release().Return(nil)
	rm := repomanagermock.NewMockRepoManager(ctrl)
	rm.EXPECT().Lease(gomock.Any(), gomock.Any()).Return(ws, nil)

	o, err := NewNativeOrchestrator(context.Background(), Params{
		Storage:     st,
		RepoManager: rm,
		Logger:      zaptest.NewLogger(t),
		GitFactory:  func(dir string) git.Interface { return g },
		Config:      testConfig(t),
	})
	require.NoError(t, err)
	reader, err := o.GetTargetGraph(context.Background(), entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "1234567890"},
	})
	require.NoError(t, err)
	require.NotNil(t, reader)
	defer reader.Close()
	chunk, rerr := reader.Read()
	require.NoError(t, rerr)
	require.NotNil(t, chunk.Targets)
	_, rerr = reader.Read()
	assert.Equal(t, io.EOF, rerr)
}

func TestNative_GetTargetGraph_TreehashNotFound_NoError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()
	st := storagemock.NewMockStorage(ctrl)
	// First attempt returns NotFound to trigger compute path.
	st.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{}, storage.NewNotFoundError("missing"))
	// Expect writes (graph stream and background treehash-mapping upload).
	st.EXPECT().Put(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, req storage.UploadRequest) error {
		_, err := io.Copy(io.Discard, req.Reader)
		return err
	}).MinTimes(1).MaxTimes(3)
	// After compute, second read returns a valid delimited stream with one message
	var buf bytes.Buffer
	_ = gob.NewEncoder(&buf).Encode(entity.GetTargetGraphResponse{Targets: []entity.OptimizedTarget{{ID: 1}}})
	st.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{
		ReadCloser: io.NopCloser(bytes.NewReader(buf.Bytes())),
	}, nil)
	g := gitmock.NewMockInterface(ctrl)
	g.EXPECT().RevParse(gomock.Any(), "HEAD^{tree}").Return("th", nil)
	ws := workspacemock.NewMockWorkspace(ctrl)
	ws.EXPECT().Path().Return("/tmp/ws")
	ws.EXPECT().Checkout(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	ws.EXPECT().ApplyRequests(gomock.Any(), gomock.Any()).Return(nil)
	ws.EXPECT().Release().Return(nil)
	rm := repomanagermock.NewMockRepoManager(ctrl)
	rm.EXPECT().Lease(gomock.Any(), gomock.Any()).Return(ws, nil)
	graphRunner := graphmock.NewMockGraphRunner(ctrl)
	graphRunner.EXPECT().Compute(gomock.Any(), gomock.Any()).Return(targethasher.Result{Targets: map[string]*targethasher.Target{
		"//:a": &targethasher.Target{
			Name:     "//:a",
			RuleType: "go_library",
		},
	}}, nil)
	o, err := NewNativeOrchestrator(appCtx, Params{
		Storage:     st,
		RepoManager: rm,
		Logger:      zaptest.NewLogger(t),
		GitFactory:  func(dir string) git.Interface { return g },
		GraphRunner: graphRunner,
		Config:      testConfig(t),
	})
	require.Nil(t, err)
	reader, err := o.GetTargetGraph(context.Background(), entity.GetTargetGraphRequest{Build: entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "1234567890"}})
	require.NoError(t, err)
	require.NotNil(t, reader)
	defer reader.Close()
	chunk, rerr := reader.Read()
	require.NoError(t, rerr)
	require.NotNil(t, chunk.Targets)
}

func TestNative_GetTargetGraph_UnknownStrategy_UserError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	st := storagemock.NewMockStorage(ctrl)
	// The background treehash-mapping goroutine fires before the strategy check.
	st.EXPECT().Put(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	g := gitmock.NewMockInterface(ctrl)
	g.EXPECT().RevParse(gomock.Any(), "HEAD^{tree}").Return("th", nil)
	ws := workspacemock.NewMockWorkspace(ctrl)
	ws.EXPECT().Path().Return("/tmp/ws").AnyTimes()
	ws.EXPECT().Checkout(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	ws.EXPECT().ApplyRequests(gomock.Any(), gomock.Any()).Return(nil)
	ws.EXPECT().Release().Return(nil)
	rm := repomanagermock.NewMockRepoManager(ctrl)
	rm.EXPECT().Lease(gomock.Any(), gomock.Any()).Return(ws, nil)

	o, err := NewNativeOrchestrator(appCtx, Params{
		Storage:     st,
		RepoManager: rm,
		Logger:      zaptest.NewLogger(t),
		GitFactory:  func(dir string) git.Interface { return g },
		Config:      testConfig(t),
	})
	require.NoError(t, err)

	reader, err := o.GetTargetGraph(context.Background(), entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{
			Remote:   "git@github:uber/tango",
			BaseSha:  "1234567890",
			Strategy: entity.ComputationStrategy(99),
		},
		BypassCache: true,
	})
	require.Nil(t, reader)
	require.Error(t, err)
	assert.Equal(t, tangoerrors.ErrorUser, tangoerrors.GetErrorCode(err))
}

func TestNative_GetTargetGraph_RevParseError_Propagates(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	st := storagemock.NewMockStorage(ctrl)
	g := gitmock.NewMockInterface(ctrl)
	g.EXPECT().RevParse(gomock.Any(), "HEAD^{tree}").Return("", errors.New("rev-fail"))
	ws := workspacemock.NewMockWorkspace(ctrl)
	ws.EXPECT().Path().Return("/tmp/ws")
	ws.EXPECT().Checkout(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	ws.EXPECT().ApplyRequests(gomock.Any(), gomock.Any()).Return(nil)
	ws.EXPECT().Release().Return(nil)
	rm := repomanagermock.NewMockRepoManager(ctrl)
	rm.EXPECT().Lease(gomock.Any(), gomock.Any()).Return(ws, nil)
	o, err := NewNativeOrchestrator(context.Background(), Params{
		Storage:     st,
		RepoManager: rm,
		Logger:      zaptest.NewLogger(t),
		GitFactory:  func(dir string) git.Interface { return g },
		Config:      testConfig(t),
	})
	require.NoError(t, err)
	resp, err := o.GetTargetGraph(context.Background(), entity.GetTargetGraphRequest{Build: entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "1234567890"}})
	require.Error(t, err)
	require.Nil(t, resp)
}

func TestNative_GetTargetGraph_AppliesGitHubPR(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	st := storagemock.NewMockStorage(ctrl)
	var buf bytes.Buffer
	require.NoError(t, gob.NewEncoder(&buf).Encode(entity.GetTargetGraphResponse{Targets: []entity.OptimizedTarget{{ID: 1}}}))

	// git mock must handle Apply sequence from workspace.NewRequest for PR 123
	g := gitmock.NewMockInterface(ctrl)
	// Compute treehash
	g.EXPECT().RevParse(gomock.Any(), "HEAD^{tree}").Return("treehash", nil)
	// Single storage fetch for graph by remote/treehash
	st.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{
		ReadCloser: io.NopCloser(bytes.NewReader(buf.Bytes())),
	}, nil)
	ws := workspacemock.NewMockWorkspace(ctrl)
	ws.EXPECT().Path().Return("/tmp/ws")
	ws.EXPECT().Checkout(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	ws.EXPECT().ApplyRequests(gomock.Any(), gomock.Any()).Return(nil)
	ws.EXPECT().Release().Return(nil)
	rm := repomanagermock.NewMockRepoManager(ctrl)
	rm.EXPECT().Lease(gomock.Any(), gomock.Any()).Return(ws, nil)
	o, err := NewNativeOrchestrator(context.Background(), Params{
		Storage:     st,
		RepoManager: rm,
		Logger:      zaptest.NewLogger(t),
		GitFactory:  func(dir string) git.Interface { return g },
		Config:      testConfig(t),
	})
	require.NoError(t, err)
	reader, err := o.GetTargetGraph(context.Background(), entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{
			Remote:         "git@github:uber/tango",
			BaseSha:        "1234567890",
			ChangeRequests: []entity.ChangeRequest{{URL: "github://github.com/org/repo/pull/123/c3a4b5d6e7f80912a3b4c5d6e7f80912a3b4c5d6"}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, reader)
	defer reader.Close()
	chunk, rerr := reader.Read()
	require.NoError(t, rerr)
	require.NotNil(t, chunk.Targets)
}

func TestNative_GetTargetGraph_InvalidChangeURI_UserError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	st := storagemock.NewMockStorage(ctrl)
	g := gitmock.NewMockInterface(ctrl)
	ws := workspacemock.NewMockWorkspace(ctrl)
	ws.EXPECT().Path().Return("/tmp/ws")
	ws.EXPECT().Checkout(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	ws.EXPECT().Release().Return(nil)
	rm := repomanagermock.NewMockRepoManager(ctrl)
	rm.EXPECT().Lease(gomock.Any(), gomock.Any()).Return(ws, nil)
	o, err := NewNativeOrchestrator(context.Background(), Params{
		Storage:     st,
		RepoManager: rm,
		Logger:      zaptest.NewLogger(t),
		GitFactory:  func(dir string) git.Interface { return g },
		Config:      testConfig(t),
	})
	require.NoError(t, err)
	resp, err := o.GetTargetGraph(context.Background(), entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{
			Remote:  "git@github:uber/tango",
			BaseSha: "1234567890",
			// Legacy hostless form without a head SHA — rejected by the
			// native orchestrator as a user error.
			ChangeRequests: []entity.ChangeRequest{{URL: "github://org/repo/pull/123"}},
		},
	})
	require.Error(t, err)
	require.Nil(t, resp)
	assert.Equal(t, tangoerrors.ErrorUser, tangoerrors.GetErrorCode(err))
}

func TestNewNativeOrchestrator_usesProvidedConfig(t *testing.T) {
	cfg := testConfig(t)

	o, err := NewNativeOrchestrator(t.Context(), Params{Config: cfg})
	require.NoError(t, err)

	native, ok := o.(*nativeOrchestrator)
	require.True(t, ok)
	assert.Same(t, cfg, native.config)
}

func TestNewNativeOrchestrator_requiresConfig(t *testing.T) {
	_, err := NewNativeOrchestrator(t.Context(), Params{})
	require.EqualError(t, err, "config is required")
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()

	cfg, err := config.Parse("testdata/config.yaml")
	require.NoError(t, err)
	return cfg
}

func TestNative_GetTargetGraph_LeasePoolTimeout_InfraRetryable(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	rm := repomanagermock.NewMockRepoManager(ctrl)
	rm.EXPECT().Lease(gomock.Any(), gomock.Any()).Return(nil,
		fmt.Errorf("%w: %w", repomanager.ErrPoolTimeout, context.DeadlineExceeded))

	o, err := NewNativeOrchestrator(context.Background(), Params{
		RepoManager: rm,
		Logger:      zaptest.NewLogger(t),
		Config:      testConfig(t),
	})
	require.NoError(t, err)
	_, err = o.GetTargetGraph(context.Background(), entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "abc"},
	})
	require.Error(t, err)
	assert.Equal(t, tangoerrors.ErrorInfraRetryable, tangoerrors.GetErrorCode(err))
}

func TestNative_GetTargetGraph_LeaseGenericError_Infra(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	rm := repomanagermock.NewMockRepoManager(ctrl)
	rm.EXPECT().Lease(gomock.Any(), gomock.Any()).Return(nil, errors.New("clone origin failed"))

	o, err := NewNativeOrchestrator(context.Background(), Params{
		RepoManager: rm,
		Logger:      zaptest.NewLogger(t),
		Config:      testConfig(t),
	})
	require.NoError(t, err)
	_, err = o.GetTargetGraph(context.Background(), entity.GetTargetGraphRequest{
		Build: entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "abc"},
	})
	require.Error(t, err)
	assert.Equal(t, tangoerrors.ErrorInfra, tangoerrors.GetErrorCode(err))
}

// TestNative_GetTargetGraph_TGBFormat: with graph_format=tgb, a cache miss
// computes the graph, writes it as a TGB blob under the tgb key (never under
// the gob key), and returns a TGB-backed reader whose chunk stream carries
// the computed target.
func TestNative_GetTargetGraph_TGBFormat(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	st := storage.NewMemoryStorage()
	g := gitmock.NewMockInterface(ctrl)
	g.EXPECT().RevParse(gomock.Any(), "HEAD^{tree}").Return("th", nil)
	ws := workspacemock.NewMockWorkspace(ctrl)
	ws.EXPECT().Path().Return("/tmp/ws")
	ws.EXPECT().Checkout(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	ws.EXPECT().ApplyRequests(gomock.Any(), gomock.Any()).Return(nil)
	ws.EXPECT().Release().Return(nil)
	rm := repomanagermock.NewMockRepoManager(ctrl)
	rm.EXPECT().Lease(gomock.Any(), gomock.Any()).Return(ws, nil)
	graphRunner := graphmock.NewMockGraphRunner(ctrl)
	graphRunner.EXPECT().Compute(gomock.Any(), gomock.Any()).Return(targethasher.Result{
		TargetNames: []string{"//:a"},
		Targets: map[string]*targethasher.Target{
			"//:a": {
				Name:     "//:a",
				Hash:     bytes.Repeat([]byte{0xab}, 20),
				RuleType: "go_library",
			},
		},
	}, nil)

	cfg := testConfig(t)
	cfg.Service.GraphFormat = config.GraphFormatTGB

	o, err := NewNativeOrchestrator(appCtx, Params{
		Storage:     st,
		RepoManager: rm,
		Logger:      zaptest.NewLogger(t),
		GitFactory:  func(dir string) git.Interface { return g },
		GraphRunner: graphRunner,
		Config:      cfg,
	})
	require.NoError(t, err)

	build := entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "1234567890"}
	reader, err := o.GetTargetGraph(context.Background(), entity.GetTargetGraphRequest{Build: build})
	require.NoError(t, err)
	require.NotNil(t, reader)
	defer reader.Close()

	// The reader is TGB-backed: its random-access form is available.
	tgbReader, ok := reader.(*storage.TGBGraphReader)
	require.True(t, ok, "expected a *storage.TGBGraphReader, got %T", reader)
	assert.Equal(t, 1, tgbReader.TGB().NodeCount())

	// The chunk view yields the computed target.
	var sawTarget bool
	for {
		chunk, rerr := reader.Read()
		if rerr == io.EOF {
			break
		}
		require.NoError(t, rerr)
		if len(chunk.Targets) > 0 {
			sawTarget = true
		}
	}
	assert.True(t, sawTarget, "chunk stream should carry the computed target")

	// The blob landed under the tgb key only; nothing was written to the gob key.
	tgbKey := cachekey.GetTGBGraphByTreeHash(build.Remote, "th", build.Strategy, nil)
	_, err = storage.NewTGBGraphReader(context.Background(), st, tgbKey, config.DefaultMaxMessageBytes)
	require.NoError(t, err)
	gobKey := cachekey.GetGraphByTreeHash(build.Remote, "th", build.Strategy, nil)
	_, err = storage.NewGraphReader(context.Background(), st, gobKey)
	require.Error(t, err)
	assert.True(t, storage.IsNotFound(err), "gob key must stay empty under graph_format=tgb")
}

func configWithAllTargetsFiles(t *testing.T, files []string) *config.Config {
	t.Helper()

	quoted := make([]string, len(files))
	for i, f := range files {
		quoted[i] = fmt.Sprintf("      - %q", f)
	}
	yamlStr := fmt.Sprintf(`
repository:
  - remote: "git@github:uber/tango"
    all_targets_files:
%s

service:
  max_worker_pool_size: 3
  workspaces_root_path: "/tmp/tango-repo-manager"
`, strings.Join(quoted, "\n"))
	cfg, err := config.ParseBytes([]byte(yamlStr))
	require.NoError(t, err)
	return cfg
}

func TestNative_HasAllTargetsFileChange_NotConfigured_NoOp(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// No AllTargetsFiles configured for this remote: RepoManager and Storage
	// must never be touched.
	rm := repomanagermock.NewMockRepoManager(ctrl)
	st := storagemock.NewMockStorage(ctrl)

	o, err := NewNativeOrchestrator(context.Background(), Params{
		Storage:     st,
		RepoManager: rm,
		Logger:      zaptest.NewLogger(t),
		Config:      testConfig(t),
	})
	require.NoError(t, err)

	changed, err := o.HasAllTargetsFileChange(context.Background(),
		entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "sha1"},
		entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "sha2"},
	)
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestNative_HasAllTargetsFileChange_TriggerFileDiffers(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	st := storagemock.NewMockStorage(ctrl)
	// Treehash-cache reads (for first and second) miss, and the all-targets
	// result cache also isn't consulted before both treehashes are known;
	// all writes are best-effort.
	st.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{}, storage.NewNotFoundError("miss")).AnyTimes()
	st.EXPECT().Put(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	g := gitmock.NewMockInterface(ctrl)
	gomock.InOrder(
		g.EXPECT().RevParse(gomock.Any(), "HEAD^{tree}").Return("tree1", nil),
		g.EXPECT().RevParse(gomock.Any(), "HEAD^{tree}").Return("tree2", nil),
	)
	g.EXPECT().DiffWithStatus(gomock.Any(), "tree1", "tree2").Return([]git.DiffEntry{
		{Status: "M", Path: ".bazelrc"},
	}, nil)

	ws := workspacemock.NewMockWorkspace(ctrl)
	ws.EXPECT().Path().Return("/tmp/ws").AnyTimes()
	ws.EXPECT().Checkout(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(2)
	ws.EXPECT().ApplyRequests(gomock.Any(), gomock.Any()).Return(nil).Times(2)
	ws.EXPECT().Release().Return(nil)
	rm := repomanagermock.NewMockRepoManager(ctrl)
	rm.EXPECT().Lease(gomock.Any(), gomock.Any()).Return(ws, nil)

	o, err := NewNativeOrchestrator(context.Background(), Params{
		Storage:     st,
		RepoManager: rm,
		Logger:      zaptest.NewLogger(t),
		GitFactory:  func(dir string) git.Interface { return g },
		Config:      configWithAllTargetsFiles(t, []string{".bazelrc", "rules/go/sdk.bzl"}),
	})
	require.NoError(t, err)

	changed, err := o.HasAllTargetsFileChange(context.Background(),
		entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "sha1"},
		entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "sha2"},
	)
	require.NoError(t, err)
	assert.True(t, changed)
}

func TestNative_HasAllTargetsFileChange_NoTriggerFileDiffers(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	st := storagemock.NewMockStorage(ctrl)
	st.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{}, storage.NewNotFoundError("miss")).AnyTimes()
	st.EXPECT().Put(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	g := gitmock.NewMockInterface(ctrl)
	gomock.InOrder(
		g.EXPECT().RevParse(gomock.Any(), "HEAD^{tree}").Return("tree1", nil),
		g.EXPECT().RevParse(gomock.Any(), "HEAD^{tree}").Return("tree2", nil),
	)
	g.EXPECT().DiffWithStatus(gomock.Any(), "tree1", "tree2").Return([]git.DiffEntry{
		{Status: "M", Path: "some/unrelated/file.go"},
	}, nil)

	ws := workspacemock.NewMockWorkspace(ctrl)
	ws.EXPECT().Path().Return("/tmp/ws").AnyTimes()
	ws.EXPECT().Checkout(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(2)
	ws.EXPECT().ApplyRequests(gomock.Any(), gomock.Any()).Return(nil).Times(2)
	ws.EXPECT().Release().Return(nil)
	rm := repomanagermock.NewMockRepoManager(ctrl)
	rm.EXPECT().Lease(gomock.Any(), gomock.Any()).Return(ws, nil)

	o, err := NewNativeOrchestrator(context.Background(), Params{
		Storage:     st,
		RepoManager: rm,
		Logger:      zaptest.NewLogger(t),
		GitFactory:  func(dir string) git.Interface { return g },
		Config:      configWithAllTargetsFiles(t, []string{".bazelrc"}),
	})
	require.NoError(t, err)

	changed, err := o.HasAllTargetsFileChange(context.Background(),
		entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "sha1"},
		entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "sha2"},
	)
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestNative_HasAllTargetsFileChange_CacheHit_NoLease(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	build1 := entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "sha1"}
	build2 := entity.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "sha2"}
	allTargetsFiles := []string{".bazelrc"}

	st := storagemock.NewMockStorage(ctrl)
	st.EXPECT().Get(gomock.Any(), storage.DownloadRequest{Key: cachekey.GetTreehashCachePath(build1)}).
		Return(storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("tree1")))}, nil)
	st.EXPECT().Get(gomock.Any(), storage.DownloadRequest{Key: cachekey.GetTreehashCachePath(build2)}).
		Return(storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("tree2")))}, nil)
	resultKey := cachekey.GetAllTargetsChangedCachePath(build1.Remote, "tree1", "tree2", allTargetsFiles)
	st.EXPECT().Get(gomock.Any(), storage.DownloadRequest{Key: resultKey}).
		Return(storage.DownloadResponse{ReadCloser: io.NopCloser(bytes.NewReader([]byte("true")))}, nil)

	// No lease/checkout expected: the cache hit must short-circuit before
	// touching RepoManager at all.
	rm := repomanagermock.NewMockRepoManager(ctrl)

	o, err := NewNativeOrchestrator(context.Background(), Params{
		Storage:     st,
		RepoManager: rm,
		Logger:      zaptest.NewLogger(t),
		Config:      configWithAllTargetsFiles(t, allTargetsFiles),
	})
	require.NoError(t, err)

	changed, err := o.HasAllTargetsFileChange(context.Background(), build1, build2)
	require.NoError(t, err)
	assert.True(t, changed)
}
