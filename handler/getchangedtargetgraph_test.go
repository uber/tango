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
	tangoerrors "github.com/uber/tango/core/errors"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/yarpc/yarpcerrors"
	"go.uber.org/zap"
)

func TestGetChangedTargetGraph_ReturnsUnimplemented(t *testing.T) {
	h := New(Params{Logger: zap.NewNop()})

	err := h.GetChangedTargetGraph(&pb.GetChangedTargetGraphRequest{}, nil)

	require.Error(t, err)
	assert.Equal(t, yarpcerrors.CodeUnimplemented, yarpcerrors.FromError(err).Code())
	assert.Equal(t, tangoerrors.ErrorInfra, tangoerrors.GetErrorCode(err))
}
