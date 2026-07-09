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

	"github.com/uber-go/tally"
	"github.com/uber/tango/config"
	"github.com/uber/tango/core/common"
	"go.uber.org/zap"
)

var _testChunkConfig = config.ChunkConfig{
	TargetChunkSize:        common.DefaultTargetChunkSize,
	ChangedTargetChunkSize: common.DefaultChangedTargetChunkSize,
	MetadataMapChunkSize:   common.DefaultMetadataMapChunkSize,
}

func newTestController(logger *zap.Logger) *controller {
	return &controller{
		logger:                 logger,
		scope:                  tally.NoopScope,
		targetChunkSize:        common.DefaultTargetChunkSize,
		changedTargetChunkSize: common.DefaultChangedTargetChunkSize,
		metadataMapChunkSize:   common.DefaultMetadataMapChunkSize,
		totalDurationBuckets:   _totalDurationBuckets,
		appCtx:                 context.Background(),
	}
}

func mustNewController(t *testing.T, appCtx context.Context, p Params) *controller {
	t.Helper()
	if p.ChunkConfig == (config.ChunkConfig{}) {
		p.ChunkConfig = _testChunkConfig
	}
	c, err := NewController(appCtx, p)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	return c.(*controller)
}
