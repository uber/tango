package controller

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/uber/tango/core/common"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/multierr"
	"go.uber.org/zap"
)

// job represents a single goroutine of getting a target graph
type job struct {
	graphStreamChunks []*pb.GetTargetGraphResponse
	err               error
	cancelled         bool
	completed         bool
	ctx               context.Context
	cancel            context.CancelFunc
}

// GetChangedTargets returns the changed targets between two revisions.
func (c *controller) GetChangedTargets(request *pb.GetChangedTargetsRequest, stream pb.TangoServiceGetChangedTargetsYARPCServer) error {
	ctx := context.Background()
	if err := validateGetChangedTargetsRequest(request); err != nil {
		c.logger.Error("GetChangedTargets: Invalid request", zap.Error(err))
		return err
	}

	c.logger.Info("GetChangedTargets: Processing request",
		zap.String("first_revision_remote", request.GetFirstRevision().GetRemote()),
		zap.String("first_revision_base_sha", request.GetFirstRevision().GetBaseSha()),
		zap.String("second_revision_remote", request.GetSecondRevision().GetRemote()),
		zap.String("second_revision_base_sha", request.GetSecondRevision().GetBaseSha()),
	)

	jobs := make([]*job, 2)

	for i := 0; i < 2; i++ {
		// create independent contexts for each job; if one of the jobs fails, the other one should be cancelled to save resources and improve latency
		ctxNew, cancelNew := context.WithCancel(ctx)
		defer cancelNew()
		jobs[i] = &job{ctx: ctxNew, cancel: cancelNew}
	}

	// Start jobs for both revisions. Success or failure, the result will report to the results channel.
	type graphResult struct {
		// order is 0 or 1, 0 is the base (first) revision, 1 is the target (second) revision
		order int
		// TODO: pb.GetTargetGraphResponse is a stream, so most likely we can't use GetTargetGraphResponse as a return type and we'll want to read it fully before joining the threads
		graph *pb.GetTargetGraphResponse
		err   error
	}
	results := make(chan graphResult, len(jobs))

	for i := 0; i < len(jobs); i++ {
		i := i
		go func(idx int) {
			var revision *pb.BuildDescription
			if idx == 0 {
				revision = request.GetFirstRevision()
			} else {
				revision = request.GetSecondRevision()
			}
			graphReader, err := c.getGraph(jobs[idx].ctx, revision, request.GetOutputConfig())
			if err != nil {
				results <- graphResult{order: idx, err: err}
				return
			}
			if graphReader == nil {
				results <- graphResult{order: idx, err: nil}
				return
			}
			graph, err := graphReader.Read()
			results <- graphResult{order: idx, graph: graph, err: err}
		}(i)
	}

	// Wait for both results to complete, either successfully or with an error.
	for range jobs {
		select {
		case res := <-results:
			if res.graph != nil {
				jobs[res.order].graphStreamChunks = append(jobs[res.order].graphStreamChunks, res.graph)
			}
			if res.graph == nil {
				jobs[res.order].completed = true
			}
			if res.err == io.EOF {
				res.err = nil
				jobs[res.order].completed = true
			}
			if res.err != nil {
				jobs[res.order].err = res.err

				// one of the computations failed, if the other one has not completed yet, cancel it and wait for the result to come in, which would be a context cancelled result then
				other := (res.order + 1) % 2
				if !jobs[other].completed {
					jobs[other].cancel()

					// explicitly mark that this job is cancelled, so we can ignore its error later
					jobs[other].cancelled = true
				}
			}
		}
	}

	if ctx.Err() != nil {
		// If the context was cancelled by the upstream, just return the original error without additional augmentation
		return ctx.Err()
	}

	// Process errors, only aggregating the ones that are original ones and not a result of the other job being cancelled
	var err error
	for i, job := range jobs {
		if job.err != nil {
			if job.cancelled {
				// this only happens as a result of the other job failing, so we can ignore the error
				continue
			}
			err = multierr.Append(err, fmt.Errorf("failed to get target graph #%d: %w", i+1, job.err))
		}
	}

	if err != nil {
		return err
	}

	// At this point we should have both graphs computed successfully. Let's compare them.
	firstGraph := jobs[0].graphStreamChunks
	secondGraph := jobs[1].graphStreamChunks

	changedTargetsResponses, err := c.compareTargetGraphs(ctx, firstGraph, secondGraph, request.GetOutputConfig())
	if err != nil {
		c.logger.Error("GetChangedTargets: Failed to compare target graphs", zap.Error(err))
		return fmt.Errorf("failed to compare target graphs: %w", err)
	}

	for _, changedTargetsResponse := range changedTargetsResponses {
		if err := stream.Send(changedTargetsResponse); err != nil {
			c.logger.Error("GetChangedTargets: Failed to send response", zap.Error(err))
			return fmt.Errorf("failed to send response: %w", err)
		}
	}

	c.logger.Info("GetChangedTargets: Successfully processed request")
	return nil
}

func (c *controller) compareTargetGraphs(ctx context.Context, firstGraph, secondGraph []*pb.GetTargetGraphResponse, outputConfig *pb.OutputConfig) ([]*pb.GetChangedTargetsResponse, error) {
	c.logger.Info("compareTargetGraphs: Computing differences between target graphs")

	// 1) Extract targets and metadata; index by canonical names
	firstTargetsByID, firstMetadata := getTargetsAndMetadata(firstGraph)
	secondTargetsByID, secondMetadata := getTargetsAndMetadata(secondGraph)
	firstByName := buildNameIndex(firstTargetsByID, firstMetadata)
	secondByName := buildNameIndex(secondTargetsByID, secondMetadata)

	sourceFileRuleTypeID := detectSourceFileID(secondMetadata)

	// 2) Build newTargetsMap and processing order (source files first if present)
	newTargetsMap := make(map[string]*pb.OptimizedTarget, len(secondByName))
	for name, t := range secondByName {
		newTargetsMap[name] = t
	}
	changedByName := make(map[string]*pb.ChangedTarget)
	changedSourceFileTargets := make(map[string]struct{})

	// 3) Create canonical mappers for IDs (targets, rule types, tags, attributes)
	targetMapper := common.NewNameIDMapper()
	ruleTypeMapper := common.NewNameIDMapper()
	tagMapper := common.NewNameIDMapper()
	attrNameMapper := common.NewNameIDMapper()
	attrValMapper := common.NewNameIDMapper()
	// These functions are used to transpose the target into the canonical ID space.
	// When called, we attempt to find the ID for the name in the metadata and return the ID.
	getTargetId := func(name string) int32 { return targetMapper.ID(name) }
	getRuleTypeId := func(name string) int32 { return ruleTypeMapper.ID(name) }
	getTagId := func(name string) int32 { return tagMapper.ID(name) }
	getAttrNameId := func(name string) int32 { return attrNameMapper.ID(name) }
	getAttrValId := func(name string) int32 { return attrValMapper.ID(name) }

	// Identify changed targets and collect changed source files
	for name, newT := range secondByName {
		oldT, exists := firstByName[name]
		if !exists {
			// new target -> new
			changedByName[name] = &pb.ChangedTarget{
				ChangeType: pb.CHANGE_TYPE_NEW,
				NewTarget: transposeOptimizedTarget(
					newT,
					secondMetadata.GetTargetIdMapping(),
					secondMetadata.GetRuleTypeMapping(),
					secondMetadata.GetTagMapping(),
					secondMetadata.GetAttributeNameMapping(),
					secondMetadata.GetAttributeStringValueMapping(),
					getTargetId, getRuleTypeId, getTagId, getAttrNameId, getAttrValId,
				),
			}
			continue
		}
		if oldT.GetHash() == newT.GetHash() {
			// same hash -> unchanged
			continue
		}
		initial := pb.CHANGE_TYPE_INDIRECT
		// If we know the source file rule type, classify changes accordingly.
		// Otherwise, leave as UNSPECIFIED.
			// check if the target is a source file, if so, it is a direct change
		isSource := newT.GetRuleType() == sourceFileRuleTypeID && sourceFileRuleTypeID != -1
		if isSource {
			initial = pb.CHANGE_TYPE_DIRECT
			// Save the target name to the set of changed source file targets.
			// This is used to check if the source file is a direct dependencies of other targets.
			changedSourceFileTargets[name] = struct{}{}
		}
		// Transpose the target into ID, using the existing metadata mappings from the second revision.
		newTarget := transposeOptimizedTarget(
			newT,
			secondMetadata.GetTargetIdMapping(),
			secondMetadata.GetRuleTypeMapping(),
			secondMetadata.GetTagMapping(),
			secondMetadata.GetAttributeNameMapping(),
			secondMetadata.GetAttributeStringValueMapping(),
			getTargetId, getRuleTypeId, getTagId, getAttrNameId, getAttrValId,
		)
		// Transpose the target into ID. The target will be mapped to the IDs in the second revision,
		//  so the resulting IDs will be consistent with the second revision.
		oldTarget := transposeOptimizedTarget(
			oldT,
			firstMetadata.GetTargetIdMapping(),
			firstMetadata.GetRuleTypeMapping(),
			firstMetadata.GetTagMapping(),
			firstMetadata.GetAttributeNameMapping(),
			firstMetadata.GetAttributeStringValueMapping(),
			getTargetId, getRuleTypeId, getTagId, getAttrNameId, getAttrValId,
		)
		changedByName[name] = &pb.ChangedTarget{
			ChangeType: initial,
			OldTarget: oldTarget,
			NewTarget: newTarget,
		}
	}

	if sourceFileRuleTypeID != -1 {
		// Iterate over the changed targets and check if any of them are source files.
		// If so, check if any of their dependencies are changed source files.
		// If so, mark the target as a direct change.
		for name, ct := range changedByName {
			if ct.GetChangeType() == pb.CHANGE_TYPE_DIRECT {
				// Already marked as direct, skip
				continue
			}
			newT := secondByName[name]
			// Ifdependency of the target is a changed source file
			if hasDepInChangedSourceFileTargets(newT.GetDirectDependencies(), secondMetadata, changedSourceFileTargets) {
				ct.ChangeType = pb.CHANGE_TYPE_DIRECT
			}
		}
	}

	// Collect changed targets.
	changed := make([]*pb.ChangedTarget, 0, len(changedByName))
	for _, ct := range changedByName {
		changed = append(changed, ct)
	}

	// 5) Construct canonical metadata and emit responses.
	meta := &pb.Metadata{
		TargetIdMapping:              targetMapper.Invert(),
		RuleTypeMapping:              ruleTypeMapper.Invert(),
		TagMapping:                   tagMapper.Invert(),
		AttributeNameMapping:         attrNameMapper.Invert(),
		AttributeStringValueMapping:  attrValMapper.Invert(),
	}

	// Emit changes and metadata as separate responses.
	var results []*pb.GetChangedTargetsResponse
	results = append(results, &pb.GetChangedTargetsResponse{
		Item: &pb.GetChangedTargetsResponse_ChangedTargets{
			ChangedTargets: &pb.ChangedTargets{
				ChangedTargets: changed,
			},
		},
	})
	results = append(results, &pb.GetChangedTargetsResponse{
		Item: &pb.GetChangedTargetsResponse_Metadata{
			Metadata: meta,
		},
	})
	return results, nil
}

// getTargetsAndMetadata builds ID->target maps and extracts metadata from a target graph stream.
func getTargetsAndMetadata(graph []*pb.GetTargetGraphResponse) (map[int32]*pb.OptimizedTarget, *pb.Metadata) {
	targets := make(map[int32]*pb.OptimizedTarget)
	var meta *pb.Metadata
	for _, chunk := range graph {
		switch item := chunk.GetItem().(type) {
		case *pb.GetTargetGraphResponse_Targets:
			for _, t := range item.Targets.GetTargets() {
				targets[t.GetId()] = t
			}
		case *pb.GetTargetGraphResponse_Metadata:
			meta = item.Metadata
		}
	}
	return targets, meta
}

// buildNameIndex creates name->target maps using the provided metadata information.
func buildNameIndex(targetsByID map[int32]*pb.OptimizedTarget, meta *pb.Metadata) map[string]*pb.OptimizedTarget {
	byName := make(map[string]*pb.OptimizedTarget, len(targetsByID))
	for id, t := range targetsByID {
		name, err := canonicalTargetName(id, meta)
		if err != nil {
			// If a target ID is missing in metadata, skip it.
			continue
		}
		byName[name] = t
	}
	return byName
}

// detectSourceFileID returns the literal rule type name for source file if present.
func detectSourceFileID(meta *pb.Metadata) int32 {
	if meta == nil || len(meta.GetRuleTypeMapping()) == 0 {
		return -1
	}
	// check the id in the rule type mapping for "source file"
	for id, name := range meta.GetRuleTypeMapping() {
		if name == "source file" {
			return id
		}
	}
	return -1
}

// canonicalTargetName returns a stable identifier for a target using metadata mapping when available.
func canonicalTargetName(id int32, meta *pb.Metadata) (string, error) {
	if meta != nil {
		if name, ok := meta.GetTargetIdMapping()[id]; ok && name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("target id %d not found in metadata", id)
}

// hasDepInChangedSourceFileTargets returns true if any dependency (resolved via metadata) is a changed source file target.
func hasDepInChangedSourceFileTargets(depIds []int32, meta *pb.Metadata, changedSourceFileTargets map[string]struct{}) bool {
	if meta == nil {
		return false
	}
	for _, id := range depIds {
		name := meta.GetTargetIdMapping()[id]
		if name == "" {
			continue
		}
		if _, ok := changedSourceFileTargets[name]; ok {
			return true
		}
	}
	return false
}

// transposeOptimizedTarget remaps a target into the canonical ID space using name-based mappers.
func transposeOptimizedTarget(
	src *pb.OptimizedTarget,
	oldTargetIdMap map[int32]string,
	oldRuleTypeIdMap map[int32]string,
	oldTagIdMap map[int32]string,
	attrNameIdMap map[int32]string,
	attrValIdMap map[int32]string,
	getTargetId func(string) int32,
	getRuleTypeId func(string) int32,
	getTagId func(string) int32,
	getAttrNameId func(string) int32,
	getAttrValId func(string) int32,
) *pb.OptimizedTarget {
	if src == nil {
		return nil
	}
	dst := &pb.OptimizedTarget{
		Id:       getTargetId(oldTargetIdMap[src.GetId()]),
		Hash:     src.GetHash(),
		Root:     src.GetRoot(),
		External: src.GetExternal(),
	}
	// Direct deps
	deps := src.GetDirectDependencies()
	if len(deps) > 0 {
		out := make([]int32, 0, len(deps))
		for _, d := range deps {
			out = append(out, getTargetId(oldTargetIdMap[d]))
		}
		dst.DirectDependencies = out
	}
	// Rule type
	if rtName := oldRuleTypeIdMap[src.GetRuleType()]; rtName != "" {
		dst.RuleType = getRuleTypeId(rtName)
	}
	// Tags
	if tags := src.GetTags(); len(tags) > 0 {
		out := make([]int32, 0, len(tags))
		for _, tg := range tags {
			out = append(out, getTagId(oldTagIdMap[tg]))
		}
		dst.Tags = out
	}
	// Attributes
	if attrs := src.GetAttributes(); len(attrs) > 0 {
		out := make(map[int32]int32, len(attrs))
		for k, v := range attrs {
			name := attrNameIdMap[k]
			val := attrValIdMap[v]
			out[getAttrNameId(name)] = getAttrValId(val)
		}
		dst.Attributes = out
	}
	return dst
}

func validateGetChangedTargetsRequest(request *pb.GetChangedTargetsRequest) error {
	if request == nil {
		return errors.New("request cannot be nil")
	}
	if request.GetFirstRevision() == nil {
		return errors.New("first revision is required")
	}
	if request.GetSecondRevision() == nil {
		return errors.New("second revision is required")
	}
	firstRevision := request.GetFirstRevision()
	if firstRevision.GetRemote() == "" {
		return errors.New("first revision remote is required")
	}
	if firstRevision.GetBaseSha() == "" {
		return errors.New("first revision base_sha is required")
	}
	secondRevision := request.GetSecondRevision()
	if secondRevision.GetRemote() == "" {
		return errors.New("second revision remote is required")
	}
	if secondRevision.GetBaseSha() == "" {
		return errors.New("second revision base_sha is required")
	}
	// Validate that both revisions have the same remote
	if firstRevision.GetRemote() != secondRevision.GetRemote() {
		return errors.New("first and second revision must have the same remote")
	}
	return nil
}
