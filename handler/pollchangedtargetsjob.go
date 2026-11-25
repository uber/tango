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
	"time"

	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/mapper/proto"
	"github.com/uber/tango/observability/metrics"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/zap"
)

func (h *handler) PollChangedTargetsJob(ctx context.Context, request *pb.PollJobRequest) (*pb.PollChangedTargetsJobResponse, error) {
	start := time.Now()
	status, chunks, err := h.controller.PollChangedTargetsJob(ctx, request.GetToken())
	repo := status.RepositoryID
	if repo == "" {
		repo = unknownRepositoryMetricLabel
	}
	e := h.emitter.Tagged(map[string]string{metrics.TagRepo: repo})
	metrics.BeginAt(e, opPollChangedTargetsJob, metrics.SlowDurationBuckets, start).Complete(err)
	if err != nil {
		h.logger.Error("PollChangedTargetsJob failed", append(tangoerrors.Fields(err), zap.String("repository", repo), zap.String("job_token", request.GetToken()))...)
		return nil, toWireError(err)
	}

	resp := &pb.PollChangedTargetsJobResponse{Status: proto.JobStatusToProto(status)}
	maxDist := int32(-1)
	if request.GetOutputConfig() != nil {
		maxDist = request.GetOutputConfig().GetMaxDistance()
	}
	for i := range chunks {
		mergeChangedTargetsChunk(resp, trimChangedTargetsResponse(&chunks[i], maxDist, request.GetOutputConfig()))
	}
	return resp, nil
}

func mergeChangedTargetsChunk(dst *pb.PollChangedTargetsJobResponse, r *pb.GetChangedTargetsResponse) {
	switch item := r.GetItem().(type) {
	case *pb.GetChangedTargetsResponse_ChangedTargets:
		if dst.ChangedTargets == nil {
			dst.ChangedTargets = &pb.ChangedTargets{}
		}
		dst.ChangedTargets.ChangedTargets = append(dst.ChangedTargets.ChangedTargets, item.ChangedTargets.GetChangedTargets()...)
	case *pb.GetChangedTargetsResponse_Metadata:
		dst.Metadata = mergeMetadata(dst.Metadata, item.Metadata)
	}
}
