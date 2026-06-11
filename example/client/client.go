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

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"go.uber.org/yarpc"
	yarpcgrpc "go.uber.org/yarpc/transport/grpc"
	"go.uber.org/zap"

	pb "github.com/uber/tango/tangopb"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8081", "server address (gRPC inbound)")
	method := flag.String("method", "get-target-graph", "method to call: get-target-graph, get-changed-targets")
	remote := flag.String("remote", "", "build description remote")
	baseSHA := flag.String("base-sha", "", "build description base sha")
	reqURLs := flag.String("request-urls", "", "comma-separated change request URLs")
	timeout := flag.Duration("timeout", 5*time.Minute, "request timeout")
	maxDistance := flag.Int("max-distance", -1, "max distance for changed targets")
	computeDistances := flag.Bool("compute-distances", false, "compute distances for changed targets")
	bypassCache := flag.Bool("bypass-cache", false, "skip cache lookup and force recomputation, overwriting cached result")

	newBaseSHA := flag.String("new-base-sha", "", "build description new base sha")
	newRequestURLs := flag.String("new-request-urls", "", "comma-separated change request URLs for new state")
	flag.Parse()

	grpcTransport := yarpcgrpc.NewTransport()
	out := grpcTransport.NewSingleOutbound(*addr)
	zl, _ := zap.NewDevelopment()
	defer zl.Sync()
	logger := zl.Sugar()
	dispatcher := yarpc.NewDispatcher(yarpc.Config{
		Name: "tango-client",
		Outbounds: yarpc.Outbounds{
			"tango": {Stream: out},
		},
	})
	if err := dispatcher.Start(); err != nil {
		logger.Errorf("start dispatcher: %w", err)
		os.Exit(1)
	}
	defer dispatcher.Stop()

	client := pb.NewTangoYARPCClient(dispatcher.ClientConfig("tango"))

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	switch *method {
	case "get-target-graph":
		var requests []*pb.Request
		if trimmed := strings.TrimSpace(*reqURLs); trimmed != "" {
			for _, u := range strings.Split(trimmed, ",") {
				if v := strings.TrimSpace(u); v != "" {
					requests = append(requests, &pb.Request{Url: v})
				}
			}
		}
		req := &pb.GetTargetGraphRequest{
			BuildDescription: &pb.BuildDescription{
				Remote:   *remote,
				BaseSha:  *baseSHA,
				Requests: requests,
			},
			BypassCache: *bypassCache,
		}
		if err := callGetTargetGraph(ctx, client, logger, req); err != nil {
			// log error and exit
			logger.Errorf("Error: %v", err)
			os.Exit(1)
		}
	case "get-changed-targets":
		var requests []*pb.Request
		// check if both reqURLs and newRequestURLs are provided
		if *baseSHA == "" && *newBaseSHA == "" {
			logger.Errorf("Error: both baseSHA and newBaseSHA cannot be empty")
			os.Exit(1)
		}
		if trimmed := strings.TrimSpace(*reqURLs); trimmed != "" {
			for _, u := range strings.Split(trimmed, ",") {
				if v := strings.TrimSpace(u); v != "" {
					requests = append(requests, &pb.Request{Url: v})
				}
			}
		}
		var newRequests []*pb.Request
		if trimmed := strings.TrimSpace(*newRequestURLs); trimmed != "" {
			for _, u := range strings.Split(trimmed, ",") {
				if v := strings.TrimSpace(u); v != "" {
					newRequests = append(newRequests, &pb.Request{Url: v})
				}
			}
		}
		req := &pb.GetChangedTargetsRequest{
			FirstRevision: &pb.BuildDescription{
				Remote:   *remote,
				BaseSha:  *baseSHA,
				Requests: requests,
			},
			SecondRevision: &pb.BuildDescription{
				Remote:   *remote,
				BaseSha:  *newBaseSHA,
				Requests: newRequests,
			},
			OutputConfig: &pb.OutputConfig{
				ComputeDistances: *computeDistances,
				MaxDistance:      int32(*maxDistance),
			},
			BypassCache: *bypassCache,
		}
		if err := callGetChangedTargets(ctx, client, logger, req); err != nil {
			logger.Errorf("Error: %v", err)
			os.Exit(1)
		}
	default:
		logger.Errorf("unsupported method: %s", *method)
		os.Exit(1)
	}
	logger.Info("Done.")
}

func callGetTargetGraph(ctx context.Context, client pb.TangoYARPCClient, logger *zap.SugaredLogger, req *pb.GetTargetGraphRequest) error {
	stream, err := client.GetTargetGraph(ctx, req)
	if err != nil {
		return fmt.Errorf("GetTargetGraph: %w", err)
	}
	defer stream.CloseSend()

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}
		if msg == nil {
			logger.Warn("Received empty message")
			return nil
		}
		if msg.Item == nil {
			logger.Warn("Received empty item")
			return nil
		}
		switch x := msg.Item.(type) {
		case *pb.GetTargetGraphResponse_Targets:
			logger.Infof("Received targets packet with %d targets", len(x.Targets.GetTargets()))
			j, err := json.Marshal(x.Targets)
			if err != nil {
				return fmt.Errorf("marshal targets: %w", err)
			}
			fmt.Println(string(j))
		case *pb.GetTargetGraphResponse_Metadata:
			logger.Info("Metadata:")
			j, err := json.Marshal(x.Metadata)
			if err != nil {
				return fmt.Errorf("marshal metadata: %w", err)
			}
			fmt.Println(string(j))
		default:
			logger.Warn("Received unknown item")
		}
	}
	return nil
}

func callGetChangedTargets(ctx context.Context, client pb.TangoYARPCClient, logger *zap.SugaredLogger, req *pb.GetChangedTargetsRequest) error {
	stream, err := client.GetChangedTargets(ctx, req)
	if err != nil {
		return fmt.Errorf("GetChangedTargets: %w", err)
	}
	defer stream.CloseSend()

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}
		if msg == nil {
			logger.Info("Received empty message")
			return nil
		}
		if msg.Item == nil {
			logger.Warn("Received empty item")
			return nil
		}
		switch x := msg.Item.(type) {
		case *pb.GetChangedTargetsResponse_ChangedTargets:
			logger.Infof("Received changed targets packet with %d targets", len(x.ChangedTargets.GetChangedTargets()))
			j, err := json.Marshal(x.ChangedTargets)
			if err != nil {
				return fmt.Errorf("marshal changed targets: %w", err)
			}
			fmt.Println(string(j))
		case *pb.GetChangedTargetsResponse_Metadata:
			logger.Info("Metadata:")
			j, err := json.Marshal(x.Metadata)
			if err != nil {
				return fmt.Errorf("marshal metadata: %w", err)
			}
			fmt.Println(string(j))
		}
	}
	return nil
}
