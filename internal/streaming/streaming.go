package streaming

import (
	"fmt"

	"github.com/uber/tango/entity"
)

// Sizer is satisfied by any type that reports its serialized byte length.
type Sizer interface {
	Size() int
}

// SplitBySize splits items into consecutive runs whose cumulative Size()
// stays at or under maxBytes. A single item larger than the budget ships
// alone since it can't be split further. Always returns at least one
// group: empty input yields a single empty group so callers always have a
// message to send on the stream. Returns an error if a multi-group split
// produces any empty group after the first.
func SplitBySize[T Sizer](items []T, maxBytes int) ([][]T, error) {
	if len(items) == 0 {
		return [][]T{nil}, nil
	}
	groups := make([][]T, 0, 1)
	var current []T
	currentBytes := 0
	for _, item := range items {
		itemBytes := item.Size()
		if len(current) > 0 && currentBytes+itemBytes > maxBytes {
			groups = append(groups, current)
			current = nil
			currentBytes = 0
		}
		current = append(current, item)
		currentBytes += itemBytes
	}
	groups = append(groups, current)
	for i := 1; i < len(groups); i++ {
		if len(groups[i]) == 0 {
			return nil, fmt.Errorf("internal error: group %d of %d is empty", i, len(groups))
		}
	}
	return groups, nil
}

// SplitMetadata splits the metadata maps into multiple Metadata
// messages so each stays at or under maxBytes. The two large maps (target
// names, attribute string values) are split independently by measured
// entry wire size; consumers merge all metadata before use. The small
// maps (rule_type, tag, attribute_name) are sent in the first message.
// Always returns at least one message. Returns an error if a non-first
// message is completely empty.
func SplitMetadata(
	targetIDToName map[int32]string,
	ruleTypeIDToName map[int32]string,
	tagIDToName map[int32]string,
	attrNameIDToName map[int32]string,
	attrStrValIDToVal map[int32]string,
	maxBytes int,
) ([]*entity.Metadata, error) {
	targetGroups := splitMapByBytes(targetIDToName, maxBytes)
	attrValGroups := splitMapByBytes(attrStrValIDToVal, maxBytes)

	metas := make([]*entity.Metadata, 0, max(1, len(targetGroups)+len(attrValGroups)))
	for _, g := range targetGroups {
		metas = append(metas, &entity.Metadata{TargetIDMapping: g})
	}
	for _, g := range attrValGroups {
		metas = append(metas, &entity.Metadata{AttributeStringValueMapping: g})
	}
	if len(metas) == 0 {
		metas = append(metas, &entity.Metadata{})
	}
	metas[0].RuleTypeMapping = ruleTypeIDToName
	metas[0].TagMapping = tagIDToName
	metas[0].AttributeNameMapping = attrNameIDToName

	for i := 1; i < len(metas); i++ {
		m := metas[i]
		if len(m.TargetIDMapping) == 0 &&
			len(m.RuleTypeMapping) == 0 &&
			len(m.TagMapping) == 0 &&
			len(m.AttributeNameMapping) == 0 &&
			len(m.AttributeStringValueMapping) == 0 {
			return nil, fmt.Errorf("internal error: metadata group %d of %d is empty", i, len(metas))
		}
	}

	return metas, nil
}

func splitMapByBytes(m map[int32]string, maxBytes int) []map[int32]string {
	if len(m) == 0 {
		return nil
	}
	var groups []map[int32]string
	current := make(map[int32]string)
	currentBytes := 0
	for k, v := range m {
		entryBytes := mapEntryWireSize(k, v)
		if len(current) > 0 && currentBytes+entryBytes > maxBytes {
			groups = append(groups, current)
			current = make(map[int32]string)
			currentBytes = 0
		}
		current[k] = v
		currentBytes += entryBytes
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

func mapEntryWireSize(k int32, v string) int {
	mapEntrySize := 1 + varintSize(uint64(k)) + 1 + len(v) + varintSize(uint64(len(v)))
	return mapEntrySize + 1 + varintSize(uint64(mapEntrySize))
}

func varintSize(x uint64) int {
	n := 1
	for x >= 0x80 {
		x >>= 7
		n++
	}
	return n
}

// SplitTargetGraph splits targets and metadata into wire-safe
// entity.GetTargetGraphResponse chunks bounded by maxMessageBytes.
func SplitTargetGraph(targets []entity.OptimizedTarget, meta *entity.Metadata, maxMessageBytes int) ([]entity.GetTargetGraphResponse, error) {
	var chunks []entity.GetTargetGraphResponse
	start := 0
	currentBytes := 0
	for i := range targets {
		itemBytes := targets[i].Size()
		if i > start && currentBytes+itemBytes > maxMessageBytes {
			chunks = append(chunks, entity.GetTargetGraphResponse{
				Targets: targets[start:i],
			})
			start = i
			currentBytes = 0
		}
		currentBytes += itemBytes
	}
	chunks = append(chunks, entity.GetTargetGraphResponse{
		Targets: targets[start:],
	})

	metaGroups, err := SplitMetadata(
		meta.TargetIDMapping,
		meta.RuleTypeMapping,
		meta.TagMapping,
		meta.AttributeNameMapping,
		meta.AttributeStringValueMapping,
		maxMessageBytes,
	)
	if err != nil {
		return nil, err
	}
	for _, m := range metaGroups {
		chunks = append(chunks, entity.GetTargetGraphResponse{Metadata: m})
	}

	return chunks, nil
}
