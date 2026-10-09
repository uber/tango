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

package handler

import (
	"fmt"
	"time"

	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/mapper"
	"github.com/uber/tango/internal/streaming"
	"github.com/uber/tango/mapper/proto"
	"github.com/uber/tango/observability/metrics"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/zap"
)

// GetChangedTargets returns the changed targets between two revisions. If the
// client disconnects, the stream's context is cancelled and the function
// returns with context.Canceled.
func (h *handler) GetChangedTargets(request *pb.GetChangedTargetsRequest, stream pb.TangoServiceGetChangedTargetsYARPCServer) (retErr error) {
	entityReq, mappingErr := proto.ProtoToGetChangedTargetsRequest(request)
	if mappingErr != nil {
		mappingErr = tangoerrors.NewUser(fmt.Errorf("convert get changed targets request: %w", mappingErr))
	}
	repoCfg, repo, repositoryErr := h.resolveRequestRepository(entityReq.First.Remote, mappingErr)
	e := h.emitter.Tagged(map[string]string{metrics.TagRepo: repo})
	op := metrics.Begin(e, opGetChangedTargets, metrics.SlowDurationBuckets)
	logger := h.logger.WithLazy(zap.String("repository", repo))
	defer func() {
		op.Complete(retErr)
		if retErr != nil {
			logger.Error("GetChangedTargets failed", tangoerrors.Fields(retErr)...)
			retErr = toWireError(retErr)
		}
	}()
	if repositoryErr != nil {
		return repositoryErr
	}

	ctx := stream.Context()
	start := time.Now()
	logger.Info("GetChangedTargets: Processing request")

	// Default max_distance to -1 (no filtering) when the client omits OutputConfig
	// entirely. When OutputConfig is supplied, take max_distance at face value -
	// see proto/tango.proto OutputConfig.max_distance for the wire-default caveat.
	maxDist := int32(-1)
	if request.GetOutputConfig() != nil {
		maxDist = request.GetOutputConfig().GetMaxDistance()
	}

	result, err := h.controller.GetChangedTargets(ctx, entityReq, repoCfg)
	if err != nil {
		return fmt.Errorf("get changed targets: %w", err)
	}

	responses, err := streaming.ChunkChangedTargetsResult(result, h.maxMessageBytes)
	if err != nil {
		return fmt.Errorf("chunk response: %w", err)
	}

	sendStart := time.Now()
	if err := sendTrimmedChangedTargets(stream, responses, maxDist, request.GetOutputConfig()); err != nil {
		return fmt.Errorf("send response: %w", err)
	}
	sendDuration := time.Since(sendStart)
	e.DurationHistogram(opGetChangedTargets, "send_duration", metrics.FastDurationBuckets).RecordDuration(sendDuration)

	logger.Info("GetChangedTargets: Successfully processed request",
		zap.Duration("send_duration", sendDuration),
		zap.Duration("total_duration", time.Since(start)),
	)
	return nil
}

// sendTrimmedChangedTargets streams responses to the client, filtering changed targets to those
// within maxDist from any distance-0 seed when maxDist >= 0, stripping per-target
// hash/tags/attributes per outputConfig's include_* flags, and pruning metadata mappings
// whose IDs are no longer referenced. Each entity response is converted to proto at the
// stream.Send boundary.
func sendTrimmedChangedTargets(stream pb.TangoServiceGetChangedTargetsYARPCServer, responses []entity.GetChangedTargetsResponse, maxDist int32, outputConfig *pb.OutputConfig) error {
	stripFields := optimizedTargetNeedsStripping(outputConfig)
	pruneMeta := metadataNeedsPruning(outputConfig)
	for i := range responses {
		protoResp := mapper.ChangedTargetsResponseToProto(&responses[i])
		toSend := protoResp
		switch item := protoResp.GetItem().(type) {
		case *pb.GetChangedTargetsResponse_ChangedTargets:
			if maxDist >= 0 || stripFields {
				kept := item.ChangedTargets.GetChangedTargets()
				if maxDist >= 0 {
					kept = filterChangedTargetsByDistance(kept, maxDist)
				}
				kept = applyChangedTargetsOutputConfig(kept, outputConfig)
				toSend = &pb.GetChangedTargetsResponse{
					Item: &pb.GetChangedTargetsResponse_ChangedTargets{
						ChangedTargets: &pb.ChangedTargets{ChangedTargets: kept},
					},
				}
			}
		case *pb.GetChangedTargetsResponse_Metadata:
			if pruneMeta {
				toSend = &pb.GetChangedTargetsResponse{
					Item: &pb.GetChangedTargetsResponse_Metadata{
						Metadata: applyMetadataOutputConfig(item.Metadata, outputConfig),
					},
				}
			}
		}
		if err := stream.Send(toSend); err != nil {
			return err
		}
	}
	return nil
}
