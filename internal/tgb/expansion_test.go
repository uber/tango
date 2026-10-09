package tgb_test

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/tgb"
)

// taglessGraph builds an n-node graph in which no target has tags, like a repo
// whose rules never set them. Everything else varies so that TAG_DEG is the
// only highly compressible column.
func taglessGraph(n int) *tgb.Graph {
	rng := rand.New(rand.NewSource(1))
	idMap := make(map[int32]string, n)
	targets := make([]entity.OptimizedTarget, n)
	for i := range targets {
		id := int32(i + 1)
		idMap[id] = fmt.Sprintf("//pkg/p%d:t%d", rng.Intn(n/3+1), i)
		raw := make([]byte, 8)
		rng.Read(raw)
		attrs := map[int32]int32{}
		if rng.Intn(4) == 0 {
			attrs[0] = int32(rng.Intn(3))
		}
		var deps []int32
		for j := rng.Intn(4); i > 0 && j > 0; j-- {
			deps = append(deps, int32(rng.Intn(i)+1))
		}
		targets[i] = entity.OptimizedTarget{
			ID:                 id,
			Hash:               hex.EncodeToString(raw),
			DirectDependencies: deps,
			RuleType:           int32(rng.Intn(4)),
			Attributes:         attrs,
		}
	}
	return &tgb.Graph{
		Targets: targets,
		Metadata: entity.Metadata{
			TargetIDMapping:             idMap,
			RuleTypeMapping:             map[int32]string{0: "source file", 1: "js_library", 2: "ts_project", 3: "js_test"},
			AttributeNameMapping:        map[int32]string{0: "visibility"},
			AttributeStringValueMapping: map[int32]string{0: "//visibility:public", 1: "true", 2: "false"},
		},
	}
}

// TestTaglessGraphReadable guards against rejecting graphs whose per-node
// columns are all zeros. With no tags TAG_DEG is nodeCount zero bytes, which
// zstd compresses to a near-constant ~100 bytes, so its expansion ratio grows
// with nodeCount without bound even though the blob is valid.
func TestTaglessGraphReadable(t *testing.T) {
	const nodes = 700_000
	data := mustEncode(t, taglessGraph(nodes), tgb.EncodeOptions{HashBytes: 8, BlockSize: 512})

	r, err := tgb.NewReader(data)
	require.NoError(t, err, "valid tagless graph must be readable")
	assert.Equal(t, nodes, r.NodeCount())

	// Keep the test honest: it only covers the bug if TAG_DEG expands past
	// the 4096x per-column ratio cap this reader used to enforce.
	stats, err := tgb.ColumnStats(data)
	require.NoError(t, err)
	var found bool
	for _, s := range stats {
		if s.Name != "TAG_DEG" {
			continue
		}
		found = true
		require.NotZero(t, s.CompressedSize)
		require.Greater(t, s.RawSize/s.CompressedSize, uint64(4096),
			"TAG_DEG no longer exceeds 4096x; grow nodes")
	}
	require.True(t, found, "TAG_DEG column missing")

	g, err := tgb.Decode(data)
	require.NoError(t, err)
	assert.Len(t, g.Targets, nodes)
}

// withColumnRawSize returns data with the directory's rawSize for column id
// replaced and the directory CRC recomputed, as a hostile blob would.
func withColumnRawSize(t *testing.T, data []byte, id, rawSize uint64) []byte {
	t.Helper()
	const footerSize = 16
	dirOff := binary.LittleEndian.Uint64(data[len(data)-footerSize:])
	dir := data[dirOff : len(data)-footerSize]

	next := func() uint64 {
		v, n := binary.Uvarint(dir)
		require.Positive(t, n, "truncated directory")
		dir = dir[n:]
		return v
	}
	count := next()
	newDir := binary.AppendUvarint(nil, count)
	var patched bool
	for i := uint64(0); i < count; i++ {
		colID := next()
		codec := dir[0]
		dir = dir[1:]
		offset, comp, raw := next(), next(), next()
		if colID == id {
			raw, patched = rawSize, true
		}
		newDir = binary.AppendUvarint(newDir, colID)
		newDir = append(newDir, codec)
		newDir = binary.AppendUvarint(newDir, offset)
		newDir = binary.AppendUvarint(newDir, comp)
		newDir = binary.AppendUvarint(newDir, raw)
	}
	require.True(t, patched, "column %d not in directory", id)

	out := append([]byte(nil), data[:dirOff]...)
	out = append(out, newDir...)
	out = binary.LittleEndian.AppendUint64(out, dirOff)
	out = binary.LittleEndian.AppendUint32(out, crc32.Checksum(newDir, crc32.MakeTable(crc32.Castagnoli)))
	return append(out, "TGB1"...)
}

// TestHostileColumnExpansionStillRejected checks that, without a per-column
// ratio cap, a small blob still cannot claim a huge column.
func TestHostileColumnExpansionStillRejected(t *testing.T) {
	const tagDeg = 10
	data := mustEncode(t, buildTinyGraph(), tgb.EncodeOptions{HashBytes: 8, BlockSize: 4})

	_, err := tgb.NewReader(data)
	require.NoError(t, err, "unmodified blob must be readable")

	_, err = tgb.NewReader(withColumnRawSize(t, data, tagDeg, 1<<28))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "total raw bytes")
}
