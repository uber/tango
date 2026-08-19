package mapper

import (
	"context"
	"encoding/hex"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/uber/tango/core/targethasher"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/mapper/idmapper"
	"github.com/uber/tango/internal/streaming"
)

const cancelCheckInterval = 4096

// ResultToTargetGraph converts a targethasher.Result into ID-mapped entity
// types. Returns a flat list of targets and their accompanying metadata.
// No chunking or proto conversion is performed.
func ResultToTargetGraph(ctx context.Context, result targethasher.Result) ([]entity.OptimizedTarget, *entity.Metadata, error) {
	targetNamesMapping := make(map[string]int32, len(result.TargetNames))
	for i, name := range result.TargetNames {
		targetNamesMapping[name] = int32(i + 1)
	}

	ruleTypeMapper := idmapper.NewMapper()
	tagMapper := idmapper.NewMapper()
	attrNameMapper := idmapper.NewMapper()
	attrStrValMapper := idmapper.NewMapper()

	targets := make([]entity.OptimizedTarget, 0, len(result.TargetNames))

	n := 0
	for _, name := range result.TargetNames {
		t, ok := result.Targets[name]
		if !ok {
			continue
		}
		if n%cancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, nil, context.Cause(ctx)
			}
		}
		n++

		depIDs := make([]int32, 0, len(t.Deps))
		for _, depName := range t.Deps {
			if depID, ok := targetNamesMapping[depName]; ok {
				depIDs = append(depIDs, depID)
			}
		}

		idt := entity.OptimizedTarget{
			ID:                 targetNamesMapping[name],
			Hash:               hex.EncodeToString(t.Hash),
			DirectDependencies: depIDs,
			Root:               t.Root,
			External:           t.External,
		}
		if t.RuleType != "" {
			idt.RuleType = ruleTypeMapper.ID(t.RuleType)
		}
		if len(t.Tags) > 0 {
			tagIDs := make([]int32, 0, len(t.Tags))
			for _, tag := range t.Tags {
				tagIDs = append(tagIDs, tagMapper.ID(tag))
			}
			idt.Tags = tagIDs
		}
		if len(t.Attributes) > 0 {
			attrs := make(map[int32]int32, len(t.Attributes))
			for _, attr := range t.Attributes {
				if attr.GetType() == buildpb.Attribute_STRING && attr.Name != nil && attr.StringValue != nil {
					attrs[attrNameMapper.ID(*attr.Name)] = attrStrValMapper.ID(*attr.StringValue)
				}
			}
			if len(attrs) > 0 {
				idt.Attributes = attrs
			}
		}

		targets = append(targets, idt)
	}

	targetIDToName := make(map[int32]string, len(targetNamesMapping))
	for s, id := range targetNamesMapping {
		targetIDToName[id] = s
	}

	meta := &entity.Metadata{
		TargetIDMapping:             targetIDToName,
		RuleTypeMapping:             ruleTypeMapper.Invert(),
		TagMapping:                  tagMapper.Invert(),
		AttributeNameMapping:        attrNameMapper.Invert(),
		AttributeStringValueMapping: attrStrValMapper.Invert(),
		AllTargetsFileHashes:        result.AllTargetsFileHashes,
	}

	return targets, meta, nil
}

// ResultToGraphChunks converts a targethasher.Result into the ordered
// sequence of GetTargetGraphResponse chunks ready to stream or persist. It
// maps the result to entity types (ResultToTargetGraph), then splits both the
// targets and the accompanying metadata so each chunk's serialized size stays
// at or under maxBytes. Target chunks come first, followed by metadata chunks;
// consumers merge metadata across chunks before resolving IDs.
//
// Callers that need to interleave or size chunks differently can use
// ResultToTargetGraph directly; this is the convenience path for the common
// "map, split, hand to WriteGraphStream" flow.
func ResultToGraphChunks(ctx context.Context, result targethasher.Result, maxBytes int) ([]entity.GetTargetGraphResponse, error) {
	targets, meta, err := ResultToTargetGraph(ctx, result)
	if err != nil {
		return nil, err
	}

	targetGroups, err := streaming.SplitBySize(targets, maxBytes)
	if err != nil {
		return nil, err
	}
	chunks := make([]entity.GetTargetGraphResponse, 0, len(targetGroups))
	for _, g := range targetGroups {
		chunks = append(chunks, entity.GetTargetGraphResponse{Targets: g})
	}

	metaGroups, err := streaming.SplitMetadata(
		meta.TargetIDMapping,
		meta.RuleTypeMapping,
		meta.TagMapping,
		meta.AttributeNameMapping,
		meta.AttributeStringValueMapping,
		maxBytes,
	)
	if err != nil {
		return nil, err
	}
	if len(meta.AllTargetsFileHashes) > 0 {
		metaGroups[0].AllTargetsFileHashes = meta.AllTargetsFileHashes
	}
	for _, m := range metaGroups {
		chunks = append(chunks, entity.GetTargetGraphResponse{Metadata: m})
	}

	return chunks, nil
}
