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
	"io"

	pb "github.com/uber/tango/tangopb"
)

// job represents a single goroutine fetching one target graph.
type job struct {
	graphStreamChunks []*pb.GetTargetGraphResponse
	err               error
	cancelled         bool
	completed         bool
	ctx               context.Context
	cancel            context.CancelFunc
}

// graphResult is the value sent back over the results channel from each
// fetch goroutine.
type graphResult struct {
	// order is 0 (first/base revision) or 1 (second/target revision).
	order  int
	chunks []*pb.GetTargetGraphResponse
	err    error
}

// fetchTwoGraphs concurrently retrieves the target graphs for two revisions.
// If one fetch fails, the other is cancelled to free resources and reduce
// latency. Returns the per-revision chunk lists in [first, second] order and
// an aggregated error on failure.
//
// When the caller's context is cancelled, returns ctx.Err() so callers can
// classify cancellation distinctly from real fetch failures.
func (c *controller) fetchTwoGraphs(
	ctx context.Context,
	first, second *pb.BuildDescription,
	outputConfig *pb.OutputConfig,
	requestOptions *pb.RequestOptions,
	bypassCache bool,
) ([][]*pb.GetTargetGraphResponse, error) {
	revisions := [2]*pb.BuildDescription{first, second}

	jobs := make([]*job, 2)
	for i := 0; i < 2; i++ {
		// Independent contexts let us cancel one in-flight fetch when its sibling fails.
		jobCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		jobs[i] = &job{ctx: jobCtx, cancel: cancel}
	}

	results := make(chan graphResult, len(jobs))
	for i := range jobs {
		i := i
		go func() {
			defer func() {
				if r := recover(); r != nil {
					results <- graphResult{order: i, err: fmt.Errorf("panic in graph fetch: %v", r)}
				}
			}()
			results <- c.fetchOneGraph(jobs[i].ctx, i, revisions[i], outputConfig, requestOptions, bypassCache)
		}()
	}

	// Wait for both results. Cancel the sibling as soon as one fails.
	for range jobs {
		res := <-results
		jobs[res.order].graphStreamChunks = res.chunks
		jobs[res.order].completed = true
		jobs[res.order].err = res.err
		if res.chunks == nil && res.err == nil {
			jobs[res.order].err = errors.New("no chunks returned")
		}
		if jobs[res.order].err != nil {
			other := (res.order + 1) % 2
			if !jobs[other].completed {
				jobs[other].cancel()
				jobs[other].cancelled = true
			}
		}
	}

	if ctx.Err() != nil {
		// If the upstream context was cancelled, surface that directly so the
		// caller can classify it as a user cancellation.
		return nil, ctx.Err()
	}

	// Aggregate errors, skipping siblings that were cancelled as a side effect of the other failing.
	var err error
	for i, j := range jobs {
		if j.err != nil && !j.cancelled {
			err = errors.Join(err, fmt.Errorf("failed to get target graph #%d: %w", i+1, j.err))
		}
	}
	if err != nil {
		return nil, err
	}

	graphs := make([][]*pb.GetTargetGraphResponse, 2)
	for i, j := range jobs {
		graphs[i] = j.graphStreamChunks
		// Drop references so callers can GC the chunks when finished.
		j.graphStreamChunks = nil
	}
	return graphs, nil
}

// fetchOneGraph runs the per-revision read loop for fetchTwoGraphs.
func (c *controller) fetchOneGraph(
	ctx context.Context,
	order int,
	revision *pb.BuildDescription,
	outputConfig *pb.OutputConfig,
	requestOptions *pb.RequestOptions,
	bypassCache bool,
) graphResult {
	graphReader, err := c.getGraph(ctx, revision, outputConfig, requestOptions, bypassCache)
	if err != nil || graphReader == nil {
		return graphResult{order: order, err: err}
	}
	defer graphReader.Close()

	var chunks []*pb.GetTargetGraphResponse
	for {
		chunk, err := graphReader.Read()
		if err == io.EOF {
			return graphResult{order: order, chunks: chunks}
		}
		if err != nil {
			return graphResult{order: order, err: err}
		}
		chunks = append(chunks, chunk)
	}
}
