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
	orchestratormock "github.com/uber/tango/orchestrator/orchestratormock"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
)

// TestNewController_StoresAppContext verifies the caller-supplied context is
// retained and is the one observed by background goroutines.
func TestNewController_StoresAppContext(t *testing.T) {
	ctrl := gomock.NewController(t)
	appCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := NewController(appCtx, Params{
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
