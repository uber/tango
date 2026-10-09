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
	"io"
	"time"

	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/internal/mapper"
	"github.com/uber/tango/observability/metrics"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/zap"
)

// GetTargetGraph returns the target graph for a given request.
func (h *handler) GetTargetGraph(request *pb.GetTargetGraphRequest, stream pb.TangoServiceGetTargetGraphYARPCServer) (retErr error) {
	entityReq, mappingErr := mapper.ProtoToGetTargetGraphRequest(request)
	if mappingErr != nil {
		mappingErr = tangoerrors.NewUser(fmt.Errorf("convert get target graph request: %w", mappingErr))
	}
	repoCfg, repo, repositoryErr := h.resolveRequestRepository(entityReq.Build.Remote, mappingErr)
	e := h.emitter.Tagged(map[string]string{metrics.TagRepo: repo})
	op := metrics.Begin(e, opGetTargetGraph, metrics.SlowDurationBuckets)
	logger := h.logger.WithLazy(zap.String("repository", repo))
	defer func() {
		op.Complete(retErr)
		if retErr != nil {
			logger.Error("GetTargetGraph failed", tangoerrors.Fields(retErr)...)
			retErr = toWireError(retErr)
		}
	}()
	if repositoryErr != nil {
		return repositoryErr
	}
	start := time.Now()
	ctx := stream.Context()
	graphReader, err := h.controller.GetTargetGraph(ctx, entityReq, repoCfg)
	if err != nil {
		return fmt.Errorf("get graph: %w", err)
	}
	if graphReader == nil {
		// Nothing to stream
		return nil
	}
	defer func() { _ = graphReader.Close() }()
	sendStart := time.Now()
	outputConfig := request.GetOutputConfig()
	for {
		chunk, err := graphReader.Read()
		if err == io.EOF {
			sendDuration := time.Since(sendStart)
			logger.Info("GetTargetGraph: Done streaming",
				zap.Duration("send_duration", sendDuration),
				zap.Duration("total_duration", time.Since(start)),
			)
			e.DurationHistogram(opGetTargetGraph, "send_duration", metrics.FastDurationBuckets).RecordDuration(sendDuration)
			return nil
		}
		if err != nil {
			return fmt.Errorf("graph reader read: %w", err)
		}
		protoResp := mapper.GetTargetGraphResponseToProto(&chunk)
		toSend := applyOptimizedTargetsOutputConfigToChunk(protoResp, outputConfig)
		if err := stream.Send(toSend); err != nil {
			return fmt.Errorf("send graph: %w", err)
		}
	}
}
