package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"encoding/json"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/yarpc"
	yarpcgrpc "go.uber.org/yarpc/transport/grpc"
	"go.uber.org/zap"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8081", "server address (gRPC inbound)")
	method := flag.String("method", "get-target-graph", "method to call: get-target-graph")
	remote := flag.String("remote", "", "build description remote")
	baseSHA := flag.String("base-sha", "", "build description base sha")
	reqURLs := flag.String("request-urls", "", "comma-separated change request URLs")
	timeout := flag.Duration("timeout", 5*time.Minute, "request timeout")

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
		}
		if err := callGetChangedTargets(ctx, client, logger, req); err != nil {
			logger.Errorf("Error: %v", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("Error: unsupported method: %s\n", *method)
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
			fmt.Println("Received empty message")
			return nil
		}
		if msg.Item == nil {
			fmt.Println("Received empty item")
			return nil
		}
		switch x := msg.Item.(type) {
		case *pb.GetTargetGraphResponse_Targets:
			fmt.Printf("Received targets packet with %d targets\n", len(x.Targets.GetTargets()))
			// unmarshal response to json
			json, err := json.Marshal(x.Targets)
			if err != nil {
				return fmt.Errorf("marshal targets: %w", err)
			}
			fmt.Printf("Targets: %s\n", string(json))
		case *pb.GetTargetGraphResponse_Metadata:
			// unmarshal response to json
			json, err := json.Marshal(x.Metadata)
			if err != nil {
				return fmt.Errorf("marshal metadata: %w", err)
			}
			fmt.Printf("Metadata: %s\n", string(json))
		default:
			fmt.Println("Received unknown item")
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
			fmt.Println("Received empty item")
			return nil
		}
		switch x := msg.Item.(type) {
		case *pb.GetChangedTargetsResponse_ChangedTargets:
			fmt.Printf("Received changed targets packet with %d targets\n", len(x.ChangedTargets.GetChangedTargets()))
			json, err := json.Marshal(x.ChangedTargets)
			if err != nil {
				return fmt.Errorf("marshal changed targets: %w", err)
			}
			fmt.Printf("ChangedTargets: %s\n", string(json))
		case *pb.GetChangedTargetsResponse_Metadata:
			// unmarshal response to json
			json, err := json.Marshal(x.Metadata)
			if err != nil {
				return fmt.Errorf("marshal metadata: %w", err)
			}
			fmt.Printf("Metadata: %s\n", string(json))
		}
	}
	return nil
}
