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
	"io"
	"time"

	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/internal/mapper"
	"github.com/uber/tango/mapper/proto"
	"github.com/uber/tango/observability/metrics"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/zap"
)

func (h *handler) PollTargetGraphJob(ctx context.Context, request *pb.PollJobRequest) (*pb.PollTargetGraphJobResponse, error) {
	start := time.Now()
	resp, repo, err := h.pollTargetGraphJob(ctx, request)
	if repo == "" {
		repo = unknownRepositoryMetricLabel
	}
	e := h.emitter.Tagged(map[string]string{metrics.TagRepo: repo})
	metrics.BeginAt(e, opPollTargetGraphJob, metrics.SlowDurationBuckets, start).Complete(err)
	if err != nil {
		h.logger.Error("PollTargetGraphJob failed", append(tangoerrors.Fields(err), zap.String("repository", repo), zap.String("job_token", request.GetToken()))...)
		return nil, toWireError(err)
	}
	return resp, nil
}

func (h *handler) pollTargetGraphJob(ctx context.Context, request *pb.PollJobRequest) (*pb.PollTargetGraphJobResponse, string, error) {
	status, graphReader, err := h.controller.PollTargetGraphJob(ctx, request.GetToken())
	if err != nil {
		return nil, status.RepositoryID, err
	}
	resp := &pb.PollTargetGraphJobResponse{Status: proto.JobStatusToProto(status)}
	if graphReader == nil {
		return resp, status.RepositoryID, nil
	}
	defer func() { _ = graphReader.Close() }()
	for {
		chunk, err := graphReader.Read()
		if err == io.EOF {
			return resp, status.RepositoryID, nil
		}
		if err != nil {
			return nil, status.RepositoryID, fmt.Errorf("graph reader read: %w", err)
		}
		protoResp := mapper.GetTargetGraphResponseToProto(&chunk)
		mergeTargetGraphChunk(resp, applyOptimizedTargetsOutputConfigToChunk(protoResp, request.GetOutputConfig()))
	}
}

func mergeTargetGraphChunk(dst *pb.PollTargetGraphJobResponse, r *pb.GetTargetGraphResponse) {
	switch item := r.GetItem().(type) {
	case *pb.GetTargetGraphResponse_Targets:
		if dst.Targets == nil {
			dst.Targets = &pb.OptimizedTargets{}
		}
		dst.Targets.Targets = append(dst.Targets.Targets, item.Targets.GetTargets()...)
	case *pb.GetTargetGraphResponse_Metadata:
		dst.Metadata = mergeMetadata(dst.Metadata, item.Metadata)
	}
}

func mergeMetadata(dst, src *pb.Metadata) *pb.Metadata {
	if dst == nil {
		dst = &pb.Metadata{}
	}
	dst.TargetIdMapping = mergeIDMap(dst.TargetIdMapping, src.GetTargetIdMapping())
	dst.RuleTypeMapping = mergeIDMap(dst.RuleTypeMapping, src.GetRuleTypeMapping())
	dst.TagMapping = mergeIDMap(dst.TagMapping, src.GetTagMapping())
	dst.AttributeNameMapping = mergeIDMap(dst.AttributeNameMapping, src.GetAttributeNameMapping())
	dst.AttributeStringValueMapping = mergeIDMap(dst.AttributeStringValueMapping, src.GetAttributeStringValueMapping())
	return dst
}

func mergeIDMap(dst, src map[int32]string) map[int32]string {
	if dst == nil {
		dst = make(map[int32]string, len(src))
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
