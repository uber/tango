// Copyright (c) 2026 Uber Technologies, Inc.
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

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/config"
	tangoerrors "github.com/uber/tango/core/errors"
	orchestratormock "github.com/uber/tango/orchestrator/orchestratormock"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
)

type rejectAllRepositoryConfigProvider struct{}

func (rejectAllRepositoryConfigProvider) GetRepositoryConfig(string) (config.RepositoryConfig, bool) {
	return config.RepositoryConfig{}, false
}

func TestResolveRequestRepository(t *testing.T) {
	c := &controller{repoConfig: rejectAllRepositoryConfigProvider{}}

	t.Run("request error uses unknown repository", func(t *testing.T) {
		repo, label, err := c.resolveRequestRepository("ignored", assert.AnError)
		assert.Empty(t, repo)
		assert.Equal(t, unknownRepositoryMetricLabel, label)
		assert.ErrorIs(t, err, assert.AnError)
	})

	t.Run("plain remote", func(t *testing.T) {
		_, label, err := c.resolveRequestRepository("git@github.com:other/repo.git", nil)
		require.Error(t, err)
		assert.Equal(t, unknownRepositoryMetricLabel, label)
		assert.Equal(t, tangoerrors.ErrorUser, tangoerrors.GetErrorCode(err))
	})
}

// TestNewController_StoresAppContext verifies the caller-supplied context is
// retained and is the one observed by background goroutines.
func TestNewController_StoresAppContext(t *testing.T) {
	ctrl := gomock.NewController(t)
	appCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := NewController(appCtx, Params{
		RepoConfig:   allowAnyRepositoryConfigProvider{},
		Logger:       zap.NewNop(),
		Orchestrator: orchestratormock.NewMockOrchestrator(ctrl),
	}).(*controller)

	assert.Same(t, appCtx, c.appCtx)
	assert.NoError(t, c.appCtx.Err())

	cancel()
	assert.ErrorIs(t, c.appCtx.Err(), context.Canceled)
}

// TestLinkRequestCtx_CancelsOnAppCtx verifies that the linked context is
// cancelled when the controller's appCtx is cancelled, even if the request
// context is still live.
func TestLinkRequestCtx_CancelsOnAppCtx(t *testing.T) {
	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()
	c := &controller{appCtx: appCtx}

	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()

	linked, cancelLink := c.linkRequestCtx(reqCtx)
	defer cancelLink()
	assert.NoError(t, linked.Err())

	cancelApp()
	<-linked.Done()
	assert.ErrorIs(t, linked.Err(), context.Canceled)
	assert.NoError(t, reqCtx.Err(), "linkRequestCtx must not cancel the request ctx")
	assert.Equal(t, tangoerrors.ErrorInfra, tangoerrors.GetErrorCode(context.Cause(linked)))
}

// TestLinkRequestCtx_CancelsOnRequestCtx verifies that cancellation of the
// request context propagates to the linked context.
func TestLinkRequestCtx_CancelsOnRequestCtx(t *testing.T) {
	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()
	c := &controller{appCtx: appCtx}

	reqCtx, cancelReq := context.WithCancel(context.Background())
	linked, cancelLink := c.linkRequestCtx(reqCtx)
	defer cancelLink()
	assert.NoError(t, linked.Err())

	cancelReq()
	<-linked.Done()
	assert.ErrorIs(t, linked.Err(), context.Canceled)
	assert.NoError(t, appCtx.Err(), "linkRequestCtx must not cancel the app ctx")
	assert.Equal(t, tangoerrors.ErrorCancelled, tangoerrors.GetErrorCode(context.Cause(linked)))
}

// TestLinkRequestCtx_CancelReleasesAfterFunc verifies that calling the returned
// cancel func stops the appCtx watcher so cancelling appCtx afterwards does
// not affect the now-detached linked context.
func TestLinkRequestCtx_CancelReleasesAfterFunc(t *testing.T) {
	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()
	c := &controller{appCtx: appCtx}

	linked, cancelLink := c.linkRequestCtx(context.Background())
	cancelLink()
	<-linked.Done()
	firstErr := linked.Err()
	assert.ErrorIs(t, firstErr, context.Canceled)

	// Cancelling appCtx after the linked ctx is already cancelled must be a
	// no-op (the AfterFunc handle should have been released by cancelLink).
	cancelApp()
	assert.Equal(t, firstErr, linked.Err(), "linked.Err() must not change after cancelLink")
}
