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

package handler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mock_controller "github.com/uber/tango/controller/controllermock"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/mock/gomock"
)

type fakeGetChangedTargetsStream struct {
	pb.TangoServiceGetChangedTargetsYARPCServer
}

func TestGetChangedTargetsForwardsToController(t *testing.T) {
	ctrl := mock_controller.NewMockController(gomock.NewController(t))
	h := New(Params{Controller: ctrl})

	req := &pb.GetChangedTargetsRequest{}
	stream := &fakeGetChangedTargetsStream{}
	ctrl.EXPECT().GetChangedTargets(gomock.Any(), gomock.Any()).DoAndReturn(
		func(gotReq *pb.GetChangedTargetsRequest, gotStream pb.TangoServiceGetChangedTargetsYARPCServer) error {
			assert.Same(t, req, gotReq)
			assert.Same(t, stream, gotStream)
			return nil
		})

	require.NoError(t, h.GetChangedTargets(req, stream))
}
