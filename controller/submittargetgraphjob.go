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
	"errors"
	"fmt"

	"github.com/uber/tango/config"

	"github.com/uber/tango/core/cachekey"
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/observability/metrics"
	"go.uber.org/zap"
)

// SubmitTargetGraphJob starts a GetTargetGraph job unless one is already active.
func (c *controller) SubmitTargetGraphJob(ctx context.Context, request entity.GetTargetGraphRequest, repoCfg config.RepositoryConfig) (string, entity.JobStatus, error) {
	token := cachekey.GetTargetGraphJobToken(request)
	return c.submitJob(ctx, token, cachekey.GetTargetGraphJobPath(token), repoCfg.RepositoryID, request.BypassCache,
		func(ctx context.Context, _ *zap.Logger) (jobResult, error) {
			return c.computeTargetGraph(ctx, request, repoCfg.RepositoryID)
		})
}

func (c *controller) computeTargetGraph(ctx context.Context, request entity.GetTargetGraphRequest, repositoryID string) (jobResult, error) {
	e := c.emitter.Tagged(map[string]string{metrics.TagRepo: repositoryID})
	graphReader, err := c.getGraph(ctx, e, request, repositoryID)
	if err != nil {
		return jobResult{}, err
	}
	// The graph is in storage before getGraph returns, so the job does not read it.
	if graphReader != nil {
		_ = graphReader.Close()
	}
	return c.targetGraphResult(ctx, e, request, repositoryID)
}

// targetGraphResult returns the key and format of the stored graph, as readCachedGraph reads it.
func (c *controller) targetGraphResult(ctx context.Context, e *metrics.Emitter, request entity.GetTargetGraphRequest, repositoryID string) (jobResult, error) {
	treehash, err := readTreehash(ctx, c.storage, request.Build, repositoryID, e, opGetTargetGraph)
	if err != nil {
		return jobResult{}, err
	}
	if treehash == "" {
		return jobResult{}, errors.New("treehash not found after graph fetch")
	}
	graphFormat, err := c.graphFormatFor(request.Build.Remote)
	if err != nil {
		return jobResult{}, tangoerrors.NewUser(fmt.Errorf("resolve graph config: %w", err))
	}
	if graphFormat == config.GraphFormatTGB {
		tgbPath := cachekey.GetTGBGraphByTreeHash(repositoryID, treehash, request.Build.Strategy, request.ExcludeFilesRegex)
		return jobResult{key: tgbPath, graphFormat: config.GraphFormatTGB}, nil
	}
	gobPath := cachekey.GetGraphByTreeHash(repositoryID, treehash, request.Build.Strategy, request.ExcludeFilesRegex)
	return jobResult{key: gobPath, graphFormat: config.GraphFormatGob}, nil
}

func (c *controller) openJobGraph(ctx context.Context, status entity.JobStatus) (storage.GraphReader, error) {
	if status.GraphFormat == config.GraphFormatTGB {
		return storage.NewTGBGraphReader(ctx, c.storage, status.ResultKey, c.maxMessageBytes)
	}
	return storage.NewGraphReader(ctx, c.storage, status.ResultKey)
}
