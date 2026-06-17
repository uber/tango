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
	"io"
	"testing"

	gogio "github.com/gogo/protobuf/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/core/git"
	gitmock "github.com/uber/tango/core/git/gitmock"
	repomanagermock "github.com/uber/tango/core/repomanager/mock"
	"github.com/uber/tango/core/storage"
	storagemock "github.com/uber/tango/core/storage/storagemock"
	targethasher "github.com/uber/tango/core/targethasher"
	workspacemock "github.com/uber/tango/core/workspace/workspacemock"
	graphmock "github.com/uber/tango/graphrunner/mock"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap/zaptest"
)

func TestNative_GetTargetGraph_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	st := storagemock.NewMockStorage(ctrl)
	var buf bytes.Buffer
	err := gogio.NewDelimitedWriter(&buf).WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Targets{Targets: &pb.OptimizedTargets{}},
	})
	require.NoError(t, err)
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

	o := NewNativeOrchestrator(context.Background(), Params{
		Storage:        st,
		RepoManager:    rm,
		Logger:         zaptest.NewLogger(t).Sugar(),
		GitFactory:     func(dir string) git.Interface { return g },
		ConfigFilePath: "testdata/config.yaml",
	})
	reader, err := o.GetTargetGraph(context.Background(), GetTargetGraphParam{
		Req: &pb.GetTargetGraphRequest{
			BuildDescription: &pb.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "1234567890"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, reader)
	defer reader.Close()
	graph, rerr := reader.Read()
	require.NoError(t, rerr)
	require.NotNil(t, graph)
	assert.NotNil(t, graph.GetTargets())
	graph, rerr = reader.Read()
	assert.Equal(t, io.EOF, rerr)
	assert.Nil(t, graph)
}

func TestNative_GetTargetGraph_TreehashNotFound_NoError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	st := storagemock.NewMockStorage(ctrl)
	// First attempt returns NotFound to trigger compute path.
	st.EXPECT().Get(gomock.Any(), gomock.Any()).Return(storage.DownloadResponse{}, &storage.NotFoundError{Path: "missing"})
	// Expect writes (graph list and treehash cache mapping)
	st.EXPECT().Put(gomock.Any(), gomock.Any()).Return(nil).MinTimes(2)
	// After compute, second read returns a valid delimited stream with one message
	var buf bytes.Buffer
	_ = gogio.NewDelimitedWriter(&buf).WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Targets{Targets: &pb.OptimizedTargets{}},
	})
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
	o := NewNativeOrchestrator(context.Background(), Params{
		Storage:        st,
		RepoManager:    rm,
		Logger:         zaptest.NewLogger(t).Sugar(),
		GitFactory:     func(dir string) git.Interface { return g },
		GraphRunner:    graphRunner,
		ConfigFilePath: "testdata/config.yaml",
	})
	reader, err := o.GetTargetGraph(context.Background(), GetTargetGraphParam{
		Req: &pb.GetTargetGraphRequest{BuildDescription: &pb.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "1234567890"}},
	})
	require.NoError(t, err)
	require.NotNil(t, reader)
	defer reader.Close()
	graph, rerr := reader.Read()
	require.NoError(t, rerr)
	require.NotNil(t, graph)
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
	o := NewNativeOrchestrator(context.Background(), Params{
		Storage:        st,
		RepoManager:    rm,
		Logger:         zaptest.NewLogger(t).Sugar(),
		GitFactory:     func(dir string) git.Interface { return g },
		ConfigFilePath: "testdata/config.yaml",
	})
	resp, err := o.GetTargetGraph(context.Background(), GetTargetGraphParam{
		Req: &pb.GetTargetGraphRequest{BuildDescription: &pb.BuildDescription{Remote: "git@github:uber/tango", BaseSha: "1234567890"}},
	})
	require.Error(t, err)
	require.Nil(t, resp)
}

func TestNative_GetTargetGraph_AppliesGitHubPR(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	st := storagemock.NewMockStorage(ctrl)
	var buf bytes.Buffer
	err := gogio.NewDelimitedWriter(&buf).WriteMsg(&pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Targets{Targets: &pb.OptimizedTargets{}},
	})
	require.NoError(t, err)

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
	o := NewNativeOrchestrator(context.Background(), Params{
		Storage:        st,
		RepoManager:    rm,
		Logger:         zaptest.NewLogger(t).Sugar(),
		GitFactory:     func(dir string) git.Interface { return g },
		ConfigFilePath: "testdata/config.yaml",
	})
	reader, err := o.GetTargetGraph(context.Background(), GetTargetGraphParam{
		Req: &pb.GetTargetGraphRequest{
			BuildDescription: &pb.BuildDescription{
				Remote:   "git@github:uber/tango",
				BaseSha:  "1234567890",
				Requests: []*pb.Request{{Url: "github://org/repo/pull/123"}},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, reader)
	defer reader.Close()
	graph, rerr := reader.Read()
	require.NoError(t, rerr)
	require.NotNil(t, graph)
}
