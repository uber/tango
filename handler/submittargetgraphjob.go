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
	"context"
	"fmt"

	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/internal/mapper"
	"github.com/uber/tango/mapper/proto"
	"github.com/uber/tango/observability/metrics"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/zap"
)

func (h *handler) SubmitTargetGraphJob(ctx context.Context, request *pb.GetTargetGraphRequest) (resp *pb.SubmitJobResponse, retErr error) {
	entityReq, mappingErr := mapper.ProtoToGetTargetGraphRequest(request)
	if mappingErr != nil {
		mappingErr = tangoerrors.NewUser(fmt.Errorf("convert get target graph request: %w", mappingErr))
	}
	repoCfg, repo, repositoryErr := h.resolveRequestRepository(entityReq.Build.Remote, mappingErr)
	e := h.emitter.Tagged(map[string]string{metrics.TagRepo: repo})
	op := metrics.Begin(e, opSubmitTargetGraphJob, metrics.FastDurationBuckets)
	logger := h.logger.WithLazy(zap.String("repository", repo))
	defer func() {
		op.Complete(retErr)
		if retErr != nil {
			logger.Error("SubmitTargetGraphJob failed", tangoerrors.Fields(retErr)...)
			retErr = toWireError(retErr)
		}
	}()
	if repositoryErr != nil {
		return nil, repositoryErr
	}
	token, status, err := h.controller.SubmitTargetGraphJob(ctx, entityReq, repoCfg)
	if err != nil {
		return nil, err
	}
	return &pb.SubmitJobResponse{Token: token, Status: proto.JobStatusToProto(status)}, nil
}
