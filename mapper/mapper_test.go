package mapper

import (
	"context"
	"testing"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uber/tango/core/targethasher"
	"github.com/uber/tango/entity"
)

func attrPtr(s string) *string { return &s }
func boolPtr(b bool) *bool     { return &b }
func int32Ptr(i int32) *int32  { return &i }

func discPtr(d buildpb.Attribute_Discriminator) *buildpb.Attribute_Discriminator { return &d }

func TestResultToTargetGraph_EmptyResult(t *testing.T) {
	t.Parallel()

	targets, meta, err := ResultToTargetGraph(t.Context(), targethasher.Result{})
	require.NoError(t, err)
	assert.Empty(t, targets)
	assert.NotNil(t, meta)
}

func TestResultToTargetGraph_PropagatesAllTargetsFileHashes(t *testing.T) {
	t.Parallel()

	hashes := map[string]string{".bazelrc": "abc123", "tools/bazel": "def456"}
	result := targethasher.Result{
		AllTargetsFileHashes: hashes,
	}

	_, meta, err := ResultToTargetGraph(t.Context(), result)
	require.NoError(t, err)
	assert.Equal(t, hashes, meta.AllTargetsFileHashes)
}

func TestResultToTargetGraph_NilAllTargetsFileHashes(t *testing.T) {
	t.Parallel()

	_, meta, err := ResultToTargetGraph(t.Context(), targethasher.Result{})
	require.NoError(t, err)
	assert.Nil(t, meta.AllTargetsFileHashes)
}

func TestResultToGraphChunks(t *testing.T) {
	t.Parallel()

	result := targethasher.Result{
		TargetNames: []string{"//a:a", "//b:b"},
		Targets: map[string]*targethasher.Target{
			"//a:a": {Hash: []byte{0x01}, RuleType: "go_library", Deps: []string{"//b:b"}},
			"//b:b": {Hash: []byte{0x02}, RuleType: "go_library"},
		},
	}

	t.Run("single chunk when under budget", func(t *testing.T) {
		t.Parallel()

		chunks, err := ResultToGraphChunks(t.Context(), result, 1<<20)
		require.NoError(t, err)

		var targets, metas int
		for _, c := range chunks {
			targets += len(c.Targets)
			if c.Metadata != nil {
				metas++
			}
		}
		assert.Equal(t, 2, targets)
		assert.Positive(t, metas, "expected at least one metadata chunk")
	})

	t.Run("splits targets across chunks under a tiny budget", func(t *testing.T) {
		t.Parallel()

		chunks, err := ResultToGraphChunks(t.Context(), result, 1)
		require.NoError(t, err)

		targetChunks := 0
		total := 0
		for _, c := range chunks {
			if len(c.Targets) > 0 {
				targetChunks++
				total += len(c.Targets)
			}
		}
		assert.Equal(t, 2, total)
		assert.Greater(t, targetChunks, 1, "tiny budget should spread targets over multiple chunks")
	})

	t.Run("propagates context cancellation", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := ResultToGraphChunks(ctx, result, 1<<20)
		require.Error(t, err)
	})

	t.Run("propagates AllTargetsFileHashes into a metadata chunk", func(t *testing.T) {
		t.Parallel()

		hashes := map[string]string{".bazelrc": "abc123"}
		result := targethasher.Result{AllTargetsFileHashes: hashes}

		chunks, err := ResultToGraphChunks(t.Context(), result, 1<<20)
		require.NoError(t, err)

		var got map[string]string
		for _, c := range chunks {
			if c.Metadata != nil && len(c.Metadata.AllTargetsFileHashes) > 0 {
				got = c.Metadata.AllTargetsFileHashes
			}
		}
		assert.Equal(t, hashes, got)
	})
}

func TestResultToTargetGraph_ScalarAttributes(t *testing.T) {
	t.Parallel()

	result := targethasher.Result{
		TargetNames: []string{"//a:a"},
		Targets: map[string]*targethasher.Target{
			"//a:a": {
				Attributes: []*buildpb.Attribute{
					{Name: attrPtr("srcs"), Type: discPtr(buildpb.Attribute_STRING), StringValue: attrPtr("main.go")},
					{Name: attrPtr("testonly"), Type: discPtr(buildpb.Attribute_BOOLEAN), BooleanValue: boolPtr(true)},
					{Name: attrPtr("size"), Type: discPtr(buildpb.Attribute_INTEGER), IntValue: int32Ptr(42)},
					{Name: attrPtr("tags"), Type: discPtr(buildpb.Attribute_STRING_LIST), StringListValue: []string{"b", "a"}},
					// Unsupported type: dropped, not stringified (even though it
					// happens to reuse the StringListValue field, its type is LABEL_LIST).
					{Name: attrPtr("deps"), Type: discPtr(buildpb.Attribute_LABEL_LIST), StringListValue: []string{"//x:x"}},
				},
			},
		},
	}

	targets, meta, err := ResultToTargetGraph(t.Context(), result)
	require.NoError(t, err)
	require.Len(t, targets, 1)

	attrs := targets[0].Attributes
	assert.Len(t, attrs, 4, "unsupported LABEL_LIST attribute should be dropped")

	got := make(map[string]string, len(attrs))
	for nameID, valID := range attrs {
		got[meta.AttributeNameMapping[nameID]] = meta.AttributeStringValueMapping[valID]
	}
	assert.Equal(t, map[string]string{
		"srcs":     "main.go",
		"testonly": "true",
		"size":     "42",
		"tags":     `["a","b"]`,
	}, got)
}

func TestResultToTargetGraph_StringListAttributeOrderInsensitive(t *testing.T) {
	t.Parallel()

	// Bazel query does not guarantee stable list ordering across runs when
	// nothing semantically changed, so two snapshots differing only in
	// order must intern to the same value (no spurious "changed" attribute).
	before := targethasher.Result{
		TargetNames: []string{"//a:a"},
		Targets: map[string]*targethasher.Target{
			"//a:a": {
				Attributes: []*buildpb.Attribute{
					{Name: attrPtr("tags"), Type: discPtr(buildpb.Attribute_STRING_LIST), StringListValue: []string{"a", "b", "c"}},
				},
			},
		},
	}
	after := targethasher.Result{
		TargetNames: []string{"//a:a"},
		Targets: map[string]*targethasher.Target{
			"//a:a": {
				Attributes: []*buildpb.Attribute{
					{Name: attrPtr("tags"), Type: discPtr(buildpb.Attribute_STRING_LIST), StringListValue: []string{"c", "a", "b"}},
				},
			},
		},
	}

	beforeTargets, beforeMeta, err := ResultToTargetGraph(t.Context(), before)
	require.NoError(t, err)
	afterTargets, afterMeta, err := ResultToTargetGraph(t.Context(), after)
	require.NoError(t, err)

	beforeAttrs := attributeValuesByName(beforeTargets[0].Attributes, beforeMeta)
	afterAttrs := attributeValuesByName(afterTargets[0].Attributes, afterMeta)
	require.Contains(t, beforeAttrs, "tags")
	require.Contains(t, afterAttrs, "tags")

	beforeVal := beforeMeta.AttributeStringValueMapping[beforeAttrs["tags"]]
	afterVal := afterMeta.AttributeStringValueMapping[afterAttrs["tags"]]
	assert.Equal(t, beforeVal, afterVal)
}

// attributeValuesByName maps attribute name to its interned value ID.
func attributeValuesByName(attrs map[int32]int32, meta *entity.Metadata) map[string]int32 {
	byName := make(map[string]int32, len(attrs))
	for nameID, valID := range attrs {
		byName[meta.AttributeNameMapping[nameID]] = valID
	}
	return byName
}
