package mapper

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/tangopb"
)

func TestChangedTargetsResponseToProto(t *testing.T) {
	t.Run("metadata", func(t *testing.T) {
		resp := entity.GetChangedTargetsResponse{
			Metadata: &entity.Metadata{
				TargetIDMapping: map[int32]string{1: "//pkg:a"},
				RuleTypeMapping: map[int32]string{10: "go_library"},
			},
		}

		got := ChangedTargetsResponseToProto(&resp)
		item, ok := got.GetItem().(*tangopb.GetChangedTargetsResponse_Metadata)
		require.True(t, ok)
		assert.Equal(t, map[int32]string{1: "//pkg:a"}, item.Metadata.GetTargetIdMapping())
		assert.Equal(t, map[int32]string{10: "go_library"}, item.Metadata.GetRuleTypeMapping())
	})

	t.Run("changed target keeps both targets and the distance", func(t *testing.T) {
		resp := entity.GetChangedTargetsResponse{
			ChangedTargets: []entity.ChangedTarget{{
				ChangeType: entity.ChangeTypeChanged,
				OldTarget:  &entity.OptimizedTarget{ID: 1, Hash: "old", DirectDependencies: []int32{2}, RuleType: 10},
				NewTarget:  &entity.OptimizedTarget{ID: 1, Hash: "new", DirectDependencies: []int32{2, 3}, RuleType: 10},
				Distance:   4,
			}},
		}

		got := ChangedTargetsResponseToProto(&resp)
		item, ok := got.GetItem().(*tangopb.GetChangedTargetsResponse_ChangedTargets)
		require.True(t, ok)
		require.Len(t, item.ChangedTargets.GetChangedTargets(), 1)
		assert.Equal(t, &tangopb.ChangedTarget{
			ChangeType: tangopb.CHANGE_TYPE_CHANGED,
			OldTarget:  &tangopb.OptimizedTarget{Id: 1, Hash: "old", DirectDependencies: []int32{2}, RuleType: 10},
			NewTarget:  &tangopb.OptimizedTarget{Id: 1, Hash: "new", DirectDependencies: []int32{2, 3}, RuleType: 10},
			Distance:   4,
		}, item.ChangedTargets.GetChangedTargets()[0])
	})
}
