package tgb

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/cespare/xxhash/v2"
	"github.com/klauspost/compress/zstd"
	"github.com/uber/tango/entity"
)

// EncodeOptions controls codec parameters.
type EncodeOptions struct {
	// ZstdLevel selects the klauspost/compress encoder level, NOT the
	// reference zstd numeric level: 1 = SpeedFastest, 2 = SpeedDefault
	// (≈ reference level 3), 3 = SpeedBetterCompression, 4 =
	// SpeedBestCompression. 0 means SpeedDefault.
	ZstdLevel int
	// HashBytes is the number of hash bytes stored per node: 8, 16, or 20.
	// Default is 8.
	HashBytes int
	// BlockSize is nodes per digest block; must be a power of two. Default 512.
	//
	// Smaller blocks cost 8 bytes of digest each but bound how much label
	// decoding a structural change triggers in phase 0: each disturbed block
	// costs about one block's worth of label decodes on both sides. 4096 was
	// the original default; at 2.8M nodes, 512 costs ~44 KB more blob and cuts
	// the per-change merge-join work 8x.
	BlockSize int
	// Serial disables per-column concurrent compression. The default is
	// parallel: columns are independent by construction, zstd dominates
	// encode time, and there is no output-order dependence (results land in
	// an indexed slice). Serial exists for profiling and for tests that want
	// deterministic single-threaded timing.
	Serial bool
}

func (o *EncodeOptions) fillDefaults() {
	if o.HashBytes == 0 {
		o.HashBytes = 8
	}
	if o.BlockSize == 0 {
		o.BlockSize = 512
	}
}

// Encode writes g to w in TGB v1 format.
func Encode(w io.Writer, g *Graph, opts EncodeOptions) error {
	opts.fillDefaults()
	if opts.HashBytes != 8 && opts.HashBytes != 16 && opts.HashBytes != 20 {
		return fmt.Errorf("tgb: HashBytes must be 8, 16, or 20; got %d", opts.HashBytes)
	}
	if opts.BlockSize <= 0 || opts.BlockSize&(opts.BlockSize-1) != 0 {
		return fmt.Errorf("tgb: BlockSize must be a power of two; got %d", opts.BlockSize)
	}

	e, err := newEncoder(g, opts)
	if err != nil {
		return err
	}
	return e.write(w)
}

// ─── encoder ─────────────────────────────────────────────────────────────────

type encoder struct {
	opts EncodeOptions
	g    *Graph

	// sorted node list
	nodes []nodeInfo

	// dictionaries (sorted strings → id)
	pkgDict      []string // sorted
	nameDict     []string
	ruleTypeDict []string
	tagDict      []string
	attrNameDict []string
	attrValDict  []string

	// per-node resolved IDs (in sorted node order)
	pkgIDs      []int32
	nameIDs     []int32
	ruleTypeIDs []int32
	hashRaw     []byte // hashBytes per node, concatenated

	// edge data — stored as pre-encoded varint byte streams
	depDeg  []int32 // out-degree per node
	depList []byte  // delta-coded dep node indices, varint-encoded
	tagDeg  []int32 // tag count per node
	tagList []byte  // delta-coded tag dict IDs, varint-encoded
	attrDeg []int32 // attr count per node
	attrNms []byte  // attr name dict IDs, varint-encoded
	attrVls []byte  // attr value dict IDs, varint-encoded

	// flags
	rootBits []byte
	extBits  []byte
	// hashEmptyBits marks nodes whose upstream Hash was the empty string.
	// tango's HashRecursively writes Hash = []byte{} (hex "") as a cycle
	// sentinel, and the fixed-stride HASH column cannot distinguish "empty"
	// from all-zeros on its own; this bitset is what makes the round-trip
	// lossless. Stored as the third bitset in the FLAGS column.
	hashEmptyBits []byte

	// block digests
	blockStarts  []int32
	blockDigests []uint64
}

// nodeInfo holds the resolved labels of one node and its original index.
type nodeInfo struct {
	label   string // full label //pkg:name
	pkg     string
	name    string
	origIdx int // index in g.Targets
	origID  int32
}

func newEncoder(g *Graph, opts EncodeOptions) (*encoder, error) {
	e := &encoder{opts: opts, g: g}
	if err := e.buildNodes(); err != nil {
		return nil, err
	}
	e.buildDicts()
	if err := e.buildColumns(); err != nil {
		return nil, err
	}
	e.buildBlocks()
	return e, nil
}

// splitLabel splits "//pkg:name" into (pkg, name).
// Labels without ':' are treated as having pkg="" and name=full_label.
// This convention is documented here: a label that contains no colon is stored
// with an empty package string so that (pkg="", name=label) round-trips
// faithfully.
func splitLabel(label string) (pkg, name string) {
	i := strings.LastIndex(label, ":")
	if i < 0 {
		return "", label
	}
	return label[:i], label[i+1:]
}

func joinLabel(pkg, name string) string {
	if pkg == "" {
		return name
	}
	return pkg + ":" + name
}

func (e *encoder) buildNodes() error {
	meta := e.g.Metadata
	e.nodes = make([]nodeInfo, len(e.g.Targets))
	for i, t := range e.g.Targets {
		label, ok := meta.TargetIDMapping[t.ID]
		if !ok {
			return fmt.Errorf("tgb: target ID %d has no label", t.ID)
		}
		pkg, name := splitLabel(label)
		e.nodes[i] = nodeInfo{
			label:   label,
			pkg:     pkg,
			name:    name,
			origIdx: i,
			origID:  t.ID,
		}
	}
	// Sort by (pkg, name) lexicographically. Parallel chunk sort: at 2.8M
	// nodes this sort is the single largest serial stage of an encode.
	parallelSortFunc(e.nodes, func(a, b nodeInfo) int {
		if c := strings.Compare(a.pkg, b.pkg); c != 0 {
			return c
		}
		return strings.Compare(a.name, b.name)
	})
	return nil
}

func sortedUniqueStrings(m map[int32]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// remapTable maps each old int32 ID from mapping to the position of its
// string in the sorted dict. This is the encode-side interning trick: the
// mapper already interned every string behind an int32, so per-node work can
// be an int32 lookup instead of a string hash. dictIdx is a transient
// string→position map over the (typically much smaller) dict.
func remapTable(mapping map[int32]string, dictIdx map[string]int32) map[int32]int32 {
	out := make(map[int32]int32, len(mapping))
	for oldID, s := range mapping {
		out[oldID] = dictIdx[s]
	}
	return out
}

func buildStrIndex(strs []string) map[string]int32 {
	m := make(map[string]int32, len(strs))
	for i, s := range strs {
		m[s] = int32(i)
	}
	return m
}

func (e *encoder) buildDicts() {
	meta := e.g.Metadata

	// Package dict and per-node package IDs in one pass: nodes are already
	// sorted by (pkg, name), so distinct packages appear as consecutive runs.
	// Assigning IDs here removes the sequential run-counter dependence from
	// buildColumns, which lets that pass shard across cores.
	e.pkgIDs = make([]int32, len(e.nodes))
	prevPkg := ""
	curPkgID := int32(-1)
	for i, n := range e.nodes {
		if i == 0 || n.pkg != prevPkg {
			e.pkgDict = append(e.pkgDict, n.pkg)
			prevPkg = n.pkg
			curPkgID++
		}
		e.pkgIDs[i] = curPkgID
	}

	// Name dict: names are only sorted within a package, so a global sort is
	// still needed; sort the full (duplicated) list and compact.
	names := make([]string, len(e.nodes))
	for i, n := range e.nodes {
		names[i] = n.name
	}
	parallelSortFunc(names, strings.Compare)
	e.nameDict = slices.Compact(names)

	e.ruleTypeDict = sortedUniqueStrings(meta.RuleTypeMapping)
	e.tagDict = sortedUniqueStrings(meta.TagMapping)
	e.attrNameDict = sortedUniqueStrings(meta.AttributeNameMapping)
	e.attrValDict = sortedUniqueStrings(meta.AttributeStringValueMapping)
}

func (e *encoder) buildColumns() error {
	meta := e.g.Metadata
	nameIdx := buildStrIndex(e.nameDict)

	// Per-node work below avoids string-keyed lookups wherever the mapper
	// already interned a string behind an int32: rule types, tags, and
	// attributes go through int32→int32 remap tables built once from the
	// metadata mappings. Package IDs need no lookup at all — nodes are
	// sorted by pkg, so IDs advance by run. Only the name column pays a
	// string-map lookup per node.
	rtRemap := remapTable(meta.RuleTypeMapping, buildStrIndex(e.ruleTypeDict))
	tagRemap := remapTable(meta.TagMapping, buildStrIndex(e.tagDict))
	anRemap := remapTable(meta.AttributeNameMapping, buildStrIndex(e.attrNameDict))
	avRemap := remapTable(meta.AttributeStringValueMapping, buildStrIndex(e.attrValDict))

	// origID → sorted node index. Corpus IDs are assigned sequentially by
	// tango's mapper, so in the common case they are dense and a slice
	// resolves each of the ~14M dependency references with an index instead
	// of a map probe. The map is kept as fallback for sparse IDs.
	var maxID int32
	for _, n := range e.nodes {
		if n.origID > maxID {
			maxID = n.origID
		}
	}
	var idToNodeSlice []int32
	var idToNodeMap map[int32]int32
	if int64(maxID) < 2*int64(len(e.nodes))+16 {
		idToNodeSlice = make([]int32, maxID+1)
		for i := range idToNodeSlice {
			idToNodeSlice[i] = -1
		}
		for i, n := range e.nodes {
			if n.origID >= 0 {
				idToNodeSlice[n.origID] = int32(i)
			}
		}
	} else {
		idToNodeMap = make(map[int32]int32, len(e.nodes))
		for i, n := range e.nodes {
			idToNodeMap[n.origID] = int32(i)
		}
	}
	resolveID := func(id int32) (int32, bool) {
		if idToNodeSlice != nil {
			if id < 0 || int(id) >= len(idToNodeSlice) || idToNodeSlice[id] < 0 {
				return 0, false
			}
			return idToNodeSlice[id], true
		}
		ni, ok := idToNodeMap[id]
		return ni, ok
	}

	n := len(e.nodes)
	// e.pkgIDs was filled by buildDicts (package IDs advance by run in the
	// sorted node order, which is a sequential dependence — everything below
	// is not, which is what lets this pass shard).
	e.nameIDs = make([]int32, n)
	e.ruleTypeIDs = make([]int32, n)
	e.hashRaw = make([]byte, n*e.opts.HashBytes)
	e.depDeg = make([]int32, n)
	e.tagDeg = make([]int32, n)
	e.attrDeg = make([]int32, n)

	bitBytes := (n + 7) / 8
	e.rootBits = make([]byte, bitBytes)
	e.extBits = make([]byte, bitBytes)
	e.hashEmptyBits = make([]byte, bitBytes)

	// Shard the per-node pass. Every output is either indexed by node
	// (disjoint writes) or a per-node varint stream with no cross-node state
	// — the first dep delta is relative to the node's own index — so shard
	// streams simply concatenate. Shard boundaries are multiples of 8 so the
	// three bitsets are byte-disjoint across shards.
	shards := runtime.GOMAXPROCS(0)
	if shards > 32 {
		shards = 32
	}
	shardLen := (n/shards + 8) &^ 7
	if shardLen < 4096 {
		shardLen = n // small graph: one shard, no goroutine overhead worth it
	}

	type shardOut struct {
		depList, tagList []byte
		attrNms, attrVls []byte
		err              error
	}
	var outs []shardOut
	var starts []int
	for lo := 0; lo < n; lo += shardLen {
		starts = append(starts, lo)
		outs = append(outs, shardOut{})
	}
	if n == 0 {
		return nil
	}

	var wg sync.WaitGroup
	for s := range starts {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			lo := starts[s]
			hi := lo + shardLen
			if hi > n {
				hi = n
			}
			out := &outs[s]

			// Scratch reused across the shard's nodes: per-node makes here
			// would cost ~3 allocations x 2.8M nodes of pure GC pressure.
			var depsBuf, tagsBuf []int32
			type kv struct{ nameID, valID int32 }
			var kvsBuf []kv

			for i := lo; i < hi; i++ {
				nd := e.nodes[i]
				t := e.g.Targets[nd.origIdx]
				e.nameIDs[i] = nameIdx[nd.name]

				// Remap lookups below use comma-ok deliberately: a missing
				// metadata mapping would otherwise remap to 0 — a *different,
				// valid* dictionary entry — and silently alias one string to
				// another. The incumbent decode path drops unresolvable IDs;
				// an encoder cannot drop and stay lossless, so it must fail.
				rtID, ok := rtRemap[t.RuleType]
				if !ok {
					out.err = fmt.Errorf("tgb: target %q: rule type ID %d has no mapping", nd.label, t.RuleType)
					return
				}
				e.ruleTypeIDs[i] = rtID

				// Hash. Three cases:
				//   "" — tango's cycle sentinel: zeros + hashEmpty bit.
				//   valid hex ≥ HashBytes — store the leading HashBytes bytes.
				//   anything else — reject. Zero-padding a short hash or
				//   skipping an unparsable one would silently corrupt the
				//   round-trip.
				if hashHex := t.Hash; hashHex == "" {
					e.hashEmptyBits[i/8] |= 1 << (uint(i) % 8)
				} else if err := hexDecodeInto(e.hashRaw[i*e.opts.HashBytes:(i+1)*e.opts.HashBytes], hashHex); err != nil {
					out.err = fmt.Errorf("tgb: target %q: %w", nd.label, err)
					return
				}

				// Deps: resolve to sorted node indices, delta-coded with the
				// first entry relative to the node's own index. A dangling
				// dependency — an ID with no target in the graph — is an
				// error, not a drop: the incumbent decode path preserves a
				// dangling dep as a label when its name mapping exists, and
				// its later disappearance from the label set is a
				// classification signal. An encoder cannot drop edges and
				// stay lossless, so it must refuse the graph instead.
				depsBuf = depsBuf[:0]
				for _, depID := range t.DirectDependencies {
					ni, ok := resolveID(depID)
					if !ok {
						out.err = fmt.Errorf("tgb: target %q: dependency ID %d has no target in the graph", nd.label, depID)
						return
					}
					depsBuf = append(depsBuf, ni)
				}
				slices.Sort(depsBuf)
				e.depDeg[i] = int32(len(depsBuf))
				for j, d := range depsBuf {
					if j == 0 {
						out.depList = appendZigzag(out.depList, int64(d)-int64(i))
					} else {
						out.depList = appendUvarint(out.depList, uint64(d-depsBuf[j-1]))
					}
				}

				// Tags.
				tagsBuf = tagsBuf[:0]
				for _, tagID := range t.Tags {
					tid, ok := tagRemap[tagID]
					if !ok {
						out.err = fmt.Errorf("tgb: target %q: tag ID %d has no mapping", nd.label, tagID)
						return
					}
					tagsBuf = append(tagsBuf, tid)
				}
				slices.Sort(tagsBuf)
				e.tagDeg[i] = int32(len(tagsBuf))
				prevTag := int32(0)
				for j, tid := range tagsBuf {
					if j == 0 {
						out.tagList = appendUvarint(out.tagList, uint64(tid))
					} else {
						out.tagList = appendUvarint(out.tagList, uint64(tid-prevTag))
					}
					prevTag = tid
				}

				// Attrs, sorted by name ID for determinism.
				e.attrDeg[i] = int32(len(t.Attributes))
				kvsBuf = kvsBuf[:0]
				for anID, avID := range t.Attributes {
					nid, ok := anRemap[anID]
					if !ok {
						out.err = fmt.Errorf("tgb: target %q: attribute name ID %d has no mapping", nd.label, anID)
						return
					}
					vid, ok := avRemap[avID]
					if !ok {
						out.err = fmt.Errorf("tgb: target %q: attribute value ID %d has no mapping", nd.label, avID)
						return
					}
					kvsBuf = append(kvsBuf, kv{nid, vid})
				}
				slices.SortFunc(kvsBuf, func(a, b kv) int { return int(a.nameID) - int(b.nameID) })
				for _, kv := range kvsBuf {
					out.attrNms = appendUvarint(out.attrNms, uint64(kv.nameID))
					out.attrVls = appendUvarint(out.attrVls, uint64(kv.valID))
				}

				// Flags.
				if t.Root {
					e.rootBits[i/8] |= 1 << (uint(i) % 8)
				}
				if t.External {
					e.extBits[i/8] |= 1 << (uint(i) % 8)
				}
			}
		}(s)
	}
	wg.Wait()

	var depTotal, tagTotal, nmTotal, vlTotal int
	for s := range outs {
		if outs[s].err != nil {
			return outs[s].err
		}
		depTotal += len(outs[s].depList)
		tagTotal += len(outs[s].tagList)
		nmTotal += len(outs[s].attrNms)
		vlTotal += len(outs[s].attrVls)
	}
	e.depList = make([]byte, 0, depTotal)
	e.tagList = make([]byte, 0, tagTotal)
	e.attrNms = make([]byte, 0, nmTotal)
	e.attrVls = make([]byte, 0, vlTotal)
	for s := range outs {
		e.depList = append(e.depList, outs[s].depList...)
		e.tagList = append(e.tagList, outs[s].tagList...)
		e.attrNms = append(e.attrNms, outs[s].attrNms...)
		e.attrVls = append(e.attrVls, outs[s].attrVls...)
	}
	return nil
}

// hexDecodeInto decodes the leading len(dst) bytes of hex string s into dst
// without allocating, and validates that the remainder of s is well-formed
// hex too — a malformed hash must fail loudly even where we don't store its
// bytes. Semantics match hex.DecodeString followed by truncation.
func hexDecodeInto(dst []byte, s string) error {
	if len(s)%2 != 0 {
		return fmt.Errorf("hash has odd hex length %d", len(s))
	}
	if len(s)/2 < len(dst) {
		return fmt.Errorf("hash is %d bytes, need at least %d", len(s)/2, len(dst))
	}
	for i := 0; i < len(dst); i++ {
		hi, ok1 := hexNibble(s[2*i])
		lo, ok2 := hexNibble(s[2*i+1])
		if !ok1 || !ok2 {
			return fmt.Errorf("hash is not hex: %q", s)
		}
		dst[i] = hi<<4 | lo
	}
	for i := 2 * len(dst); i < len(s); i++ {
		if _, ok := hexNibble(s[i]); !ok {
			return fmt.Errorf("hash is not hex: %q", s)
		}
	}
	return nil
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// decodeHashHex decodes a hex hash string and returns the first n bytes.
// If the string is shorter than 2*n hex chars, zero-pads the result.
func decodeHashHex(h string, n int) []byte {
	out := make([]byte, n)
	raw, err := hex.DecodeString(h)
	if err != nil || len(raw) == 0 {
		return out
	}
	copy(out, raw)
	return out
}

func (e *encoder) buildBlocks() {
	bsz := e.opts.BlockSize
	forceAt := bsz * 8
	n := len(e.nodes)

	blockStart := 0
	for i := 0; i < n; i++ {
		sz := i - blockStart + 1
		// content-defined cut: xxh3(label) & (blockSize-1) == 0
		h := xxhash.Sum64String(e.nodes[i].label)
		cut := (h & uint64(bsz-1)) == 0
		// force cut if block is too large
		if sz >= forceAt {
			cut = true
		}
		if cut || i == n-1 {
			// emit block digest over the labels of [blockStart..i]
			digest := e.blockDigest(blockStart, i)
			e.blockStarts = append(e.blockStarts, int32(blockStart))
			e.blockDigests = append(e.blockDigests, digest)
			blockStart = i + 1
		}
	}
}

// blockDigest computes xxhash64 over the label bytes of nodes [lo..hi].
//
// The digest certifies exactly one property: "these nodes carry these labels,
// in this order". That is all phase 0 of a comparison needs, because a matched
// block is still hash-compared in phase 1 — matching a digest aligns two runs,
// it never skips them. Deps, tags, attributes and rule types are deliberately
// excluded: a change to any of them moves the node's Merkle hash, which phase 1
// catches, and including them here would only cause blocks to stop matching for
// reasons that have nothing to do with alignment.
//
// Hashing the label *bytes* rather than the (pkgID, nameID) pair is what makes
// the digest dictionary-independent, and it is the whole point of this
// function. Dictionaries are globally sorted, so inserting a single target
// whose name sorts early shifts every subsequent nameID by one. A digest over
// dictionary IDs would therefore change for every downstream block, forcing a
// full-graph label merge-join after any structural change — and its equality
// would strictly mean "same IDs", which only implies "same labels" when both
// graphs happen to share a dictionary.
//
// Labels cannot contain NUL, so the NUL separator makes the concatenation
// unambiguous: no two distinct label sequences produce the same byte stream.
func (e *encoder) blockDigest(lo, hi int) uint64 {
	h := xxhash.New()
	var sep [1]byte
	for i := lo; i <= hi; i++ {
		_, _ = h.WriteString(e.nodes[i].label)
		_, _ = h.Write(sep[:])
	}
	return h.Sum64()
}

// ─── zstd pool ────────────────────────────────────────────────────────────────

// zstdEncPools holds one encoder pool per level. Pooling across levels would
// silently hand an encoder built at one level to a caller asking for another:
// getZstdEncoder cannot re-level a pooled encoder (Reset does not take a
// level), so the pool key must be the level itself.
var zstdEncPools sync.Map // zstd.EncoderLevel → *sync.Pool

func getZstdEncoder(level zstd.EncoderLevel) *zstd.Encoder {
	p, _ := zstdEncPools.LoadOrStore(level, &sync.Pool{})
	pool := p.(*sync.Pool)
	if v := pool.Get(); v != nil {
		return v.(*zstd.Encoder)
	}
	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(level))
	return enc
}

func putZstdEncoder(level zstd.EncoderLevel, enc *zstd.Encoder) {
	p, _ := zstdEncPools.LoadOrStore(level, &sync.Pool{})
	p.(*sync.Pool).Put(enc)
}

func compressZstd(data []byte, level zstd.EncoderLevel) []byte {
	enc := getZstdEncoder(level)
	defer putZstdEncoder(level, enc)
	return enc.EncodeAll(data, nil)
}

// ─── write ────────────────────────────────────────────────────────────────────

func (e *encoder) write(w io.Writer) error {
	level := zstd.SpeedDefault
	if e.opts.ZstdLevel != 0 {
		level = zstd.EncoderLevel(e.opts.ZstdLevel)
	}

	// Build raw column blobs.
	type rawCol struct {
		id    uint64
		codec byte
		data  []byte
	}

	// Helper: delta-coded int32 slice → varint bytes.
	encodeDeltas := func(vals []int32) []byte {
		var buf []byte
		prev := int32(0)
		for _, v := range vals {
			buf = appendUvarint(buf, uint64(v-prev))
			prev = v
		}
		return buf
	}
	encodePlain := func(vals []int32) []byte {
		var buf []byte
		for _, v := range vals {
			buf = appendUvarint(buf, uint64(v))
		}
		return buf
	}

	// NODE_PKG: delta of pkgID sequence (non-decreasing).
	nodePkgRaw := encodeDeltas(e.pkgIDs)

	// NODE_NAME: within a package, delta of nameID; across packages, absolute.
	nodeNameRaw := func() []byte {
		var buf []byte
		prevPkg := int32(-1)
		prevName := int32(0)
		for i, pid := range e.pkgIDs {
			nid := e.nameIDs[i]
			if pid != prevPkg {
				buf = appendUvarint(buf, uint64(nid))
				prevName = nid
				prevPkg = pid
			} else {
				buf = appendUvarint(buf, uint64(nid-prevName))
				prevName = nid
			}
		}
		return buf
	}()

	// BLOCK_START: delta of block first-node indices.
	blockStartRaw := encodeDeltas(e.blockStarts)

	// BLOCK_DIGEST: raw 8-byte per block.
	blockDigestRaw := make([]byte, len(e.blockDigests)*8)
	for i, d := range e.blockDigests {
		binary.LittleEndian.PutUint64(blockDigestRaw[i*8:], d)
	}

	// NODE_INDEX (column 21) is deliberately not written. It sampled byte
	// offsets into DEPS every 1024 nodes so a single node's deps could be
	// reached without scanning from the start — but no read path ever used it:
	// the reader's ensureDepOffsets builds a full per-node offset table in one
	// sequential pass, and the compare path reads deps in bulk via DepsCSR.
	// The column ID stays reserved in format.go; a reader that needs sampled
	// random access can reintroduce it.

	// FLAGS: root bitset, ext bitset, then hash-empty bitset (see hashEmptyBits).
	flagsRaw := append(append(append([]byte{}, e.rootBits...), e.extBits...), e.hashEmptyBits...)

	rawCols := []rawCol{
		{colPkgDict, codecZstd, buildFrontCodedDict(e.pkgDict)},
		{colNameDict, codecZstd, buildFrontCodedDict(e.nameDict)},
		{colNodePkg, codecZstd, nodePkgRaw},
		{colNodeName, codecZstd, nodeNameRaw},
		{colHash, codecRaw, e.hashRaw},
		{colDeg, codecZstd, encodePlain(e.depDeg)},
		{colDeps, codecZstd, e.depList},
		{colRuleType, codecZstd, encodePlain(e.ruleTypeIDs)},
		{colRuleTypeDict, codecZstd, buildFrontCodedDict(e.ruleTypeDict)},
		{colTagDeg, codecZstd, encodePlain(e.tagDeg)},
		{colTags, codecZstd, e.tagList},
		{colTagDict, codecZstd, buildFrontCodedDict(e.tagDict)},
		{colAttrDeg, codecZstd, encodePlain(e.attrDeg)},
		{colAttrName, codecZstd, e.attrNms},
		{colAttrValue, codecZstd, e.attrVls},
		{colAttrNameDict, codecZstd, buildFrontCodedDict(e.attrNameDict)},
		{colAttrValueDict, codecZstd, buildFrontCodedDict(e.attrValDict)},
		{colFlags, codecZstd, flagsRaw},
		{colBlockDigest, codecRaw, blockDigestRaw},
		{colBlockStart, codecZstd, blockStartRaw},
	}

	if len(e.g.Metadata.AllTargetsFileHashes) > 0 {
		rawCols = append(rawCols, rawCol{colAllTargetsFileHashes, codecZstd, encodeStringMap(e.g.Metadata.AllTargetsFileHashes)})
	}

	// Compress zstd columns (optionally parallel).
	type compressedCol struct {
		id    uint64
		codec byte
		raw   []byte
		comp  []byte
	}
	results := make([]compressedCol, len(rawCols))

	compress := func(idx int, rc rawCol) {
		var comp []byte
		if rc.codec == codecZstd && len(rc.data) > 0 {
			comp = compressZstd(rc.data, level)
		} else {
			comp = rc.data
		}
		results[idx] = compressedCol{rc.id, rc.codec, rc.data, comp}
	}

	if !e.opts.Serial {
		var wg sync.WaitGroup
		wg.Add(len(rawCols))
		for i, rc := range rawCols {
			i, rc := i, rc
			go func() {
				defer wg.Done()
				compress(i, rc)
			}()
		}
		wg.Wait()
	} else {
		for i, rc := range rawCols {
			compress(i, rc)
		}
	}

	// Build header.
	edgeCount := uint64(0)
	for _, d := range e.depDeg {
		edgeCount += uint64(d)
	}
	hdr := fileHeader{
		nodeCount: uint64(len(e.nodes)),
		edgeCount: edgeCount,
		blockSize: uint64(e.opts.BlockSize),
		hashBytes: uint16(e.opts.HashBytes),
	}
	hdrBytes := encodeHeader(hdr)

	// Assign offsets.
	offset := uint64(headerSize)
	entries := make([]colEntry, 0, len(results))
	for _, r := range results {
		rawSz := uint64(len(r.raw))
		compSz := uint64(len(r.comp))
		entries = append(entries, colEntry{
			id:             r.id,
			codec:          r.codec,
			offset:         offset,
			compressedSize: compSz,
			rawSize:        rawSz,
		})
		offset += compSz
	}

	// Encode directory.
	dirBytes := encodeDirectory(entries)
	dirCRC := crc32.Checksum(dirBytes, crc32.MakeTable(crc32.Castagnoli))

	ftr := fileFooter{
		dirOffset: offset,
		dirCRC:    dirCRC,
	}
	ftrBytes := encodeFooter(ftr)

	// Write everything.
	if _, err := w.Write(hdrBytes[:]); err != nil {
		return err
	}
	for _, r := range results {
		if _, err := w.Write(r.comp); err != nil {
			return err
		}
	}
	if _, err := w.Write(dirBytes); err != nil {
		return err
	}
	if _, err := w.Write(ftrBytes[:]); err != nil {
		return err
	}
	return nil
}

// encodeStringMap serializes a map[string]string as a length-prefixed sequence
// of (key, value) pairs: count (uvarint), then for each pair the key length
// (uvarint), key bytes, value length (uvarint), value bytes.
func encodeStringMap(m map[string]string) []byte {
	var buf []byte
	buf = binary.AppendUvarint(buf, uint64(len(m)))
	for k, v := range m {
		buf = binary.AppendUvarint(buf, uint64(len(k)))
		buf = append(buf, k...)
		buf = binary.AppendUvarint(buf, uint64(len(v)))
		buf = append(buf, v...)
	}
	return buf
}

// TruncateHashes returns a new Graph with each target's Hash field truncated
// to n raw bytes and re-hex-encoded. This is necessary for round-trip tests
// because TGB only stores the first n bytes of the hash, and Decode can only
// reproduce the truncated-then-rehexed value.
func TruncateHashes(g *Graph, n int) *Graph {
	targets := make([]entity.OptimizedTarget, len(g.Targets))
	for i, t := range g.Targets {
		if t.Hash != "" {
			raw := decodeHashHex(t.Hash, n)
			t.Hash = hex.EncodeToString(raw)
		}
		// "" is the cycle sentinel and round-trips as itself.
		targets[i] = t
	}
	return &Graph{
		Targets:  targets,
		Metadata: g.Metadata,
	}
}
