package tgb

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"sort"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/uber/tango/entity"
)

// Decode fully materialises a Graph from TGB data.
func Decode(data []byte) (*Graph, error) {
	r, err := NewReader(data)
	if err != nil {
		return nil, err
	}
	return r.DecodeGraph()
}

// DecodeGraph fully materialises the Graph this reader is positioned over.
// Prefer the Reader's lazy accessors when only part of the graph is needed;
// this walks and allocates every column.
func (r *Reader) DecodeGraph() (*Graph, error) {
	// Force-expand all dictionaries.
	if err := r.expandAllDicts(); err != nil {
		return nil, err
	}

	n := r.NodeCount()
	targets := make([]entity.OptimizedTarget, n)

	// Build metadata maps (inverse of what encoder built).
	targetIDMap := make(map[int32]string, n)
	ruleTypeMap := make(map[int32]string, len(r.ruleTypeDict))
	tagMap := make(map[int32]string, len(r.tagDict))
	attrNameMap := make(map[int32]string, len(r.attrNameDict))
	attrValMap := make(map[int32]string, len(r.attrValDict))

	// Fill rule-type, tag, attr dicts.
	for i, s := range r.ruleTypeDict {
		ruleTypeMap[int32(i)] = s
	}
	for i, s := range r.tagDict {
		tagMap[int32(i)] = s
	}
	for i, s := range r.attrNameDict {
		attrNameMap[int32(i)] = s
	}
	for i, s := range r.attrValDict {
		attrValMap[int32(i)] = s
	}

	// Decode per-node columns.
	if err := r.decompressAll(); err != nil {
		return nil, err
	}

	// Parse NODE_PKG, NODE_NAME.
	pkgIDs, err := r.decodePkgIDs()
	if err != nil {
		return nil, err
	}
	nameIDs, err := r.decodeNameIDs(pkgIDs)
	if err != nil {
		return nil, err
	}
	// Parse RULETYPE.
	rtIDs, err := r.decodeRuleTypeIDs()
	if err != nil {
		return nil, err
	}
	// Parse DEG / DEPS.
	_, depLists, err := r.decodeDepColumns()
	if err != nil {
		return nil, err
	}
	// Parse TAG_DEG / TAGS.
	_, tagLists, err := r.decodeTagColumns()
	if err != nil {
		return nil, err
	}
	// Parse ATTR_DEG / ATTR_NAME / ATTR_VALUE.
	_, attrNames, attrVals, err := r.decodeAttrColumns()
	if err != nil {
		return nil, err
	}
	// Parse FLAGS.
	rootBits, extBits, hashEmptyBits, err := r.decodeFlags()
	if err != nil {
		return nil, err
	}

	hb := r.HashBytes()
	hashData := r.Hashes()

	// We need a mapping from TGB node index back to a entity int32 ID.
	// The decoder assigns IDs sequentially from 0 (node index == ID for
	// the purposes of round-tripping). The original IDs are stored via
	// OriginalID, but for the Graph we just use node-index as ID.
	// This means Decode produces a graph that may have different int32 IDs
	// than the original, but labels and structure are identical.

	// Build reverse mapping: TGB node index → original entity int32 ID.
	// We do NOT have the original IDs unless the input graph was encoded
	// from an entity graph (OriginalID column not stored). For round-trip fidelity
	// we assign synthetic IDs equal to node index.
	// The round-trip test calls TruncateHashes and compares label/structure,
	// not raw int32 IDs.

	// Assign IDs: node i → ID i (0-based).
	// Build label from pkgDict / nameDict.
	for i := 0; i < n; i++ {
		pid := int(pkgIDs[i])
		nid := int(nameIDs[i])
		pkg, err := lookupDictEntry(r.pkgDict, pid)
		if err != nil {
			return nil, fmt.Errorf("tgb decode: pkg dict lookup: %w", err)
		}
		name, err := lookupDictEntry(r.nameDict, nid)
		if err != nil {
			return nil, fmt.Errorf("tgb decode: name dict lookup: %w", err)
		}
		label := joinLabel(pkg, name)
		targetIDMap[int32(i)] = label

		// Hash: read hb bytes and hex-encode — unless the node carried the
		// empty-hash cycle sentinel, which round-trips as "".
		hashStr := ""
		if hashEmptyBits == nil || (hashEmptyBits[i/8]&(1<<(uint(i)%8))) == 0 {
			rawHash := hashData[i*hb : (i+1)*hb]
			hashStr = hex.EncodeToString(rawHash)
		}

		// RuleType: look up in ruleTypeDict.
		rtID := rtIDs[i]
		// We store rule-type as the dict-index as entity int32 ID.
		// ruleTypeMap already maps dict-index → string, which is what
		// entity.Metadata.RuleTypeMapping wants.

		// Deps: convert node indices back to entity int32 IDs (== node index).
		deps := depLists[i]
		depIDs := make([]int32, len(deps))
		for j, d := range deps {
			depIDs[j] = int32(d)
		}

		// Tags: dict indices → entity tag IDs.
		tags := tagLists[i]
		tagIDs := make([]int32, len(tags))
		for j, t := range tags {
			tagIDs[j] = t // dict index is the entity tag ID
		}

		// Attrs.
		attrs := make(map[int32]int32, len(attrNames[i]))
		for j := range attrNames[i] {
			attrs[attrNames[i][j]] = attrVals[i][j]
		}

		root := rootBits != nil && (rootBits[i/8]&(1<<(uint(i)%8))) != 0
		ext := extBits != nil && (extBits[i/8]&(1<<(uint(i)%8))) != 0

		targets[i] = entity.OptimizedTarget{
			ID:                 int32(i),
			Hash:               hashStr,
			DirectDependencies: depIDs,
			RuleType:           rtID,
			Tags:               tagIDs,
			Root:               root,
			External:           ext,
			Attributes:         attrs,
		}
	}

	// Build rule-type mapping: dict-index → string (already in ruleTypeMap).
	// Tag mapping similarly.

	meta := entity.Metadata{
		TargetIDMapping:             targetIDMap,
		RuleTypeMapping:             ruleTypeMap,
		TagMapping:                  tagMap,
		AttributeNameMapping:        attrNameMap,
		AttributeStringValueMapping: attrValMap,
	}

	atfh, err := r.AllTargetsFileHashes()
	if err != nil {
		return nil, fmt.Errorf("tgb: decode AllTargetsFileHashes: %w", err)
	}
	if atfh != nil {
		meta.AllTargetsFileHashes = atfh
	}

	return &Graph{Targets: targets, Metadata: meta}, nil
}

// ─── zstd decompression ───────────────────────────────────────────────────────

// decompressZstd decodes one column, holding the decoder to the directory's
// claimed rawSize. The decoder is built per call — not pooled — because
// WithDecoderMaxMemory is the only lever that bounds DecodeAll's allocation,
// and it is fixed at construction. A pooled decoder would need a cap wide
// enough for the largest legitimate column (1 GiB), and DecodeAll preallocates
// from the frame header's *claimed* content size up to that cap before any of
// our post-decode length checks run — so a 100-byte hostile frame claiming a
// 1 GiB content size would allocate 1 GiB. Capping at rawSize (already bounded
// by validateColumns) turns that lie into an immediate error instead.
//
// The cap is floored at the encoder's maximum window size (8 MiB): the max
// memory setting also caps the acceptable frame window, and legitimate small
// columns carry windows larger than their content. An 8 MiB-per-column
// hostile allocation is the accepted worst case.
//
// Decoder construction is microseconds against a per-column DecodeAll of
// megabytes, so there is nothing worth pooling here.
func decompressZstd(data []byte, rawSize uint64) ([]byte, error) {
	const minDecoderMemory = 8 << 20 // klauspost encoder default max window
	maxMem := rawSize
	if maxMem < minDecoderMemory {
		maxMem = minDecoderMemory
	}
	dec, err := zstd.NewReader(nil,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(maxMem))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return dec.DecodeAll(data, make([]byte, 0, rawSize))
}

// ─── Reader ───────────────────────────────────────────────────────────────────

// Reader is a lazy, column-addressable view of a TGB blob.
// Constructing a Reader decompresses only the column directory.
// Each column is decompressed on first access and then cached.
type Reader struct {
	data []byte
	hdr  fileHeader
	cols map[uint64]colEntry // keyed by column ID

	mu sync.Mutex

	// decompressed column data (nil until accessed)
	decompressed map[uint64][]byte

	// decompressionCount tracks how many columns were decompressed
	// (used by tests to verify lazy behaviour).
	DecompressionCount int

	// expanded dictionaries (nil until accessed)
	pkgDict      []string
	nameDict     []string
	ruleTypeDict []string
	tagDict      []string
	attrNameDict []string
	attrValDict  []string

	// Per-node random access into the variable-length DEPS, TAGS and ATTR
	// columns requires knowing where each node's run begins. These indexes are
	// built once, lazily, on first random access.
	//
	// Without them each accessor rescans its column from offset 0 and re-decodes
	// the whole degree array, making every accessor O(nodes) instead of
	// O(degree). That is survivable in unit tests and catastrophic at monorepo
	// scale -- it cost 209s of a 213s comparison before being fixed.
	depDegs       []int32
	depByteOffset []int32
	tagDegs       []int32
	tagByteOffset []int32
	attrDegs      []int32
	attrNmOffset  []int32
	attrVlOffset  []int32

	// Decoded fixed-width per-node columns, cached on first access. These are
	// delta/varint encoded, so decoding is a whole-column operation; without
	// caching, a single Label() call re-decodes two 2.8M-entry arrays and
	// RuleType() re-decodes a third.
	pkgIDsCache      []int32
	nameIDsCache     []int32
	ruleTypeIDsCache []int32

	// Decoded BLOCK_START column, cached because BlockStart is called once
	// per block by every align pass.
	blockStartsCache []int32
}

// NewReader parses the header and column directory of data.
// No column data is decompressed.
func NewReader(data []byte) (*Reader, error) {
	if len(data) < headerSize+footerSize {
		return nil, fmt.Errorf("tgb: data too short")
	}

	// Decode header.
	var hdrArr [headerSize]byte
	copy(hdrArr[:], data[:headerSize])
	hdr, err := decodeHeader(hdrArr)
	if err != nil {
		return nil, err
	}

	// Decode footer.
	var ftrArr [footerSize]byte
	copy(ftrArr[:], data[len(data)-footerSize:])
	ftr, err := decodeFooter(ftrArr)
	if err != nil {
		return nil, err
	}

	// Validate directory CRC.
	dirEnd := len(data) - footerSize
	if uint64(dirEnd) < ftr.dirOffset {
		return nil, fmt.Errorf("tgb: directory offset out of range")
	}
	dirData := data[ftr.dirOffset:dirEnd]
	dirCRC := crc32.Checksum(dirData, crc32.MakeTable(crc32.Castagnoli))
	if dirCRC != ftr.dirCRC {
		return nil, fmt.Errorf("tgb: directory CRC mismatch")
	}

	// Parse directory.
	entries, err := decodeDirectory(dirData)
	if err != nil {
		return nil, err
	}
	cols := make(map[uint64]colEntry, len(entries))
	for _, e := range entries {
		if _, dup := cols[e.id]; dup {
			return nil, fmt.Errorf("tgb: duplicate directory entry for column %d", e.id)
		}
		cols[e.id] = e
	}

	// Validate every directory claim before anything trusts it. The CRCs
	// above only prove the directory was not corrupted in transit; a hostile
	// blob computes valid CRCs over hostile values, and every accessor after
	// this point slices r.data and sizes allocations using these fields.
	if err := validateColumns(hdr, cols, uint64(len(data)), ftr.dirOffset); err != nil {
		return nil, err
	}

	return &Reader{
		data:         data,
		hdr:          hdr,
		cols:         cols,
		decompressed: make(map[uint64][]byte),
	}, nil
}

// validateColumns enforces the structural invariants that make the lazy
// accessors safe: every column lies inside the file before the directory,
// claimed sizes are bounded, and per-node columns are consistent with the
// header's nodeCount so that nodeCount-sized allocations and fixed-stride
// slicing (HASH above all — phase 1 of the comparison slices it by node
// index with no further checks) cannot be driven past their columns.
func validateColumns(hdr fileHeader, cols map[uint64]colEntry, fileSize, dirOffset uint64) error {
	if hdr.flags&^flagRevCSR != 0 {
		return fmt.Errorf("tgb: unknown header flag bits %#x", hdr.flags&^flagRevCSR)
	}
	if hdr.blockSize != 0 && (hdr.blockSize&(hdr.blockSize-1)) != 0 {
		return fmt.Errorf("tgb: blockSize %d is not a power of two", hdr.blockSize)
	}

	var totalRaw uint64
	for id, e := range cols {
		end := e.offset + e.compressedSize
		if end < e.offset || e.offset < headerSize || end > dirOffset {
			return fmt.Errorf("tgb: column %d spans [%d, %d) outside data region [%d, %d)",
				id, e.offset, end, headerSize, dirOffset)
		}
		switch e.codec {
		case codecRaw:
			if e.rawSize != e.compressedSize {
				return fmt.Errorf("tgb: raw column %d claims rawSize %d != stored size %d",
					id, e.rawSize, e.compressedSize)
			}
		case codecZstd:
			if e.rawSize > 0 && e.compressedSize == 0 {
				return fmt.Errorf("tgb: column %d claims %d raw bytes from empty data", id, e.rawSize)
			}
			if e.compressedSize > 0 && e.rawSize > e.compressedSize*maxZstdExpansion {
				return fmt.Errorf("tgb: column %d claims %dx expansion (max %d)",
					id, e.rawSize/e.compressedSize, maxZstdExpansion)
			}
		default:
			return fmt.Errorf("tgb: column %d has unknown codec %d", id, e.codec)
		}
		if e.rawSize > maxColumnRawSize {
			return fmt.Errorf("tgb: column %d claims %d raw bytes (max %d)",
				id, e.rawSize, maxColumnRawSize)
		}
		totalRaw += e.rawSize
	}
	// The per-column expansion cap cannot stop a blob of highly-compressible
	// frames from honestly claiming raw bytes far beyond anything a real
	// graph produces; every downstream allocation is proportional to raw
	// bytes, so bound their sum against the file itself (real graphs
	// measure ~1.8x).
	if budget := max(fileSize*maxTotalRawFactor, maxTotalRawFloor); totalRaw > budget {
		return fmt.Errorf("tgb: columns claim %d total raw bytes from a %d-byte file (max %d)",
			totalRaw, fileSize, budget)
	}

	n := hdr.nodeCount
	// Tie nodeCount to real column bytes so it cannot be inflated to drive
	// nodeCount-sized allocations from a tiny blob. Every encoder-written
	// per-node varint column spends at least one raw byte per node.
	if n > 0 {
		deg, ok := cols[colDeg]
		if !ok || deg.rawSize < n {
			return fmt.Errorf("tgb: nodeCount %d inconsistent with DEG column", n)
		}
		hash, ok := cols[colHash]
		if !ok || hash.rawSize != n*uint64(hdr.hashBytes) {
			return fmt.Errorf("tgb: HASH column is %d bytes, want nodeCount %d x stride %d",
				colRawSize(cols, colHash), n, hdr.hashBytes)
		}
	}
	// Each edge costs at least one byte in DEPS.
	if hdr.edgeCount > 0 {
		deps, ok := cols[colDeps]
		if !ok || deps.rawSize < hdr.edgeCount {
			return fmt.Errorf("tgb: edgeCount %d inconsistent with DEPS column", hdr.edgeCount)
		}
	}
	// At most one digest block per node, 8 bytes each.
	if bd, ok := cols[colBlockDigest]; ok {
		if bd.rawSize%8 != 0 {
			return fmt.Errorf("tgb: BLOCK_DIGEST size %d not a multiple of 8", bd.rawSize)
		}
		if blocks := bd.rawSize / 8; blocks > n {
			return fmt.Errorf("tgb: %d digest blocks for %d nodes", blocks, n)
		}
	}
	return nil
}

func colRawSize(cols map[uint64]colEntry, id uint64) uint64 {
	if e, ok := cols[id]; ok {
		return e.rawSize
	}
	return 0
}

// validateDegs checks decoded per-node degree values against the columns they
// index into. validateColumns can only bound the DEG column's *size*; the
// degree *values* inside it are attacker-controlled varints that downstream
// code uses as allocation sizes and loop bounds, so a hostile blob can request
// a 2^31-entry list backed by a 10-byte column. The invariant that stops this
// is byte-conservation: every list entry costs at least one byte in the list
// column, so the running total of degrees can never exceed the list column's
// length.
func validateDegs(degs []int32, listLen int, what string) error {
	total := 0
	for i, d := range degs {
		if d < 0 {
			return fmt.Errorf("tgb: negative %s degree %d at node %d", what, d, i)
		}
		total += int(d)
		if total > listLen {
			return fmt.Errorf("tgb: %s degrees claim %d+ entries but list column is %d bytes", what, total, listLen)
		}
	}
	return nil
}

// NodeCount returns the number of nodes.
func (r *Reader) NodeCount() int { return int(r.hdr.nodeCount) }

// HashBytes returns the hash stride (8, 16, or 20), from the header.
func (r *Reader) HashBytes() int {
	return int(r.hdr.hashBytes)
}

// Hashes returns the raw hash bytes (stride = HashBytes). No copy; no decompression.
func (r *Reader) Hashes() []byte {
	e, ok := r.cols[colHash]
	if !ok || e.rawSize == 0 {
		return nil
	}
	// HASH column is raw (uncompressed).
	return r.data[e.offset : e.offset+e.compressedSize]
}

// BlockCount returns the number of digest blocks.
func (r *Reader) BlockCount() int {
	e, ok := r.cols[colBlockDigest]
	if !ok {
		return 0
	}
	return int(e.rawSize / 8)
}

// BlockDigests returns the block digest array.
func (r *Reader) BlockDigests() []uint64 {
	e, ok := r.cols[colBlockDigest]
	if !ok {
		return nil
	}
	raw := r.data[e.offset : e.offset+e.compressedSize]
	out := make([]uint64, len(raw)/8)
	for i := range out {
		out[i] = binary.LittleEndian.Uint64(raw[i*8:])
	}
	return out
}

// BlockStart returns the first node index of block i.
func (r *Reader) BlockStart(i int) int {
	data, err := r.getColumn(colBlockStart)
	if err != nil || len(data) == 0 {
		return 0
	}
	// Decode once and cache: align calls this for every block, and decoding
	// the whole delta-coded column per call made phase 0 O(blocks²) — the
	// dominant cost of a no-change compare at blockSize 512 (5,588 blocks).
	r.mu.Lock()
	if r.blockStartsCache == nil {
		r.blockStartsCache = decodeDeltaInts(data)
	}
	starts := r.blockStartsCache
	r.mu.Unlock()
	if i >= len(starts) {
		return 0
	}
	return int(starts[i])
}

// Label returns the full label for node i (lazy; resolves pkg + name dicts).
func (r *Reader) Label(node int) string {
	pid := r.PackageID(node)
	nid := r.NameID(node)
	r.mu.Lock()
	if err := r.ensurePkgDict(); err != nil {
		r.mu.Unlock()
		return ""
	}
	if err := r.ensureNameDict(); err != nil {
		r.mu.Unlock()
		return ""
	}
	pkg := ""
	name := ""
	if int(pid) < len(r.pkgDict) {
		pkg = r.pkgDict[pid]
	}
	if int(nid) < len(r.nameDict) {
		name = r.nameDict[nid]
	}
	r.mu.Unlock()
	return joinLabel(pkg, name)
}

// SplitLabel returns the package and name for node i as separate strings.
//
// Callers that need to compare two nodes in the encoder's node order must use
// this rather than comparing Label values. Nodes are sorted by the (pkg, name)
// tuple, and that order is not the same as lexicographic order on the joined
// label: '/' (0x2F) sorts below ':' (0x3A), so for nested packages
//
//	tuple order:  ("//a", "t") < ("//a/b", "t")
//	label order:  "//a/b:t"    < "//a:t"
//
// disagree. Nested packages are ubiquitous in Bazel, so this is the common
// case, not an edge case.
func (r *Reader) SplitLabel(node int) (pkg, name string) {
	pid := r.PackageID(node)
	nid := r.NameID(node)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensurePkgDict(); err != nil {
		return "", ""
	}
	if err := r.ensureNameDict(); err != nil {
		return "", ""
	}
	if int(pid) < len(r.pkgDict) {
		pkg = r.pkgDict[pid]
	}
	if int(nid) < len(r.nameDict) {
		name = r.nameDict[nid]
	}
	return pkg, name
}

// FindNode returns the index of the node whose label splits into (pkg, name),
// or -1 when absent. Nodes are sorted by the (pkg, name) tuple, so this is a
// binary search costing O(log n) SplitLabel calls — cheap enough to resolve
// the handful of changed labels a comparison result carries, without any
// label→index map over the whole graph.
func (r *Reader) FindNode(pkg, name string) int {
	n := r.NodeCount()
	i := sort.Search(n, func(i int) bool {
		p, nm := r.SplitLabel(i)
		if p != pkg {
			return p > pkg
		}
		return nm >= name
	})
	if i < n {
		if p, nm := r.SplitLabel(i); p == pkg && nm == name {
			return i
		}
	}
	return -1
}

// Hash returns node i's hash as the hex string tango stores upstream: the
// hex encoding of the stored hash bytes, or "" when the node carried the
// empty-hash cycle sentinel. At stride 20 this is the full SHA-1, byte for
// byte what the producer wrote.
func (r *Reader) Hash(node int) string {
	if r.HashEmpty(node) {
		return ""
	}
	hb := r.HashBytes()
	hashes := r.Hashes()
	lo := node * hb
	if lo < 0 || lo+hb > len(hashes) {
		return ""
	}
	return hex.EncodeToString(hashes[lo : lo+hb])
}

// TagName returns the tag string for a tag dictionary ID (as returned by
// Tags), or "" when out of range.
func (r *Reader) TagName(id int32) string {
	r.mu.Lock()
	_ = r.ensureTagDict()
	dict := r.tagDict
	r.mu.Unlock()
	if id < 0 || int(id) >= len(dict) {
		return ""
	}
	return dict[id]
}

// PackageID returns the package dict ID for node i.
func (r *Reader) PackageID(node int) int32 {
	ids, _ := r.getPkgIDColumn()
	if node < len(ids) {
		return ids[node]
	}
	return 0
}

// NameID returns the name dict ID for node i.
func (r *Reader) NameID(node int) int32 {
	ids, _ := r.getNameIDColumn()
	if node < len(ids) {
		return ids[node]
	}
	return 0
}

// RuleType returns the rule-type string for node i.
func (r *Reader) RuleType(node int) string {
	ids, err := r.getRuleTypeIDColumn()
	if err != nil || node >= len(ids) {
		return ""
	}
	r.mu.Lock()
	_ = r.ensureRuleTypeDict()
	dict := r.ruleTypeDict
	r.mu.Unlock()
	idx := int(ids[node])
	if idx >= len(dict) {
		return ""
	}
	return dict[idx]
}

// ensureDepOffsets builds the per-node byte-offset index into the DEPS column.
// One sequential pass over the whole column, O(nodes + edges), done at most
// once per Reader.
func (r *Reader) ensureDepOffsets() ([]int32, []int32, []byte, error) {
	degData, err := r.getColumn(colDeg)
	if err != nil {
		return nil, nil, nil, err
	}
	depsData, err := r.getColumn(colDeps)
	if err != nil {
		return nil, nil, nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.depByteOffset != nil {
		return r.depDegs, r.depByteOffset, depsData, nil
	}

	degs := decodePlainInts(degData)
	if err := validateDegs(degs, len(depsData), "dep"); err != nil {
		return nil, nil, nil, err
	}
	offsets := make([]int32, len(degs)+1)
	pos := 0
	for i, deg := range degs {
		offsets[i] = int32(pos)
		for j := int32(0); j < deg; j++ {
			var n int
			if j == 0 {
				_, n = readZigzag(depsData[pos:])
			} else {
				_, n = readUvarint(depsData[pos:])
			}
			if n <= 0 {
				return nil, nil, nil, fmt.Errorf("tgb: truncated DEPS column at node %d", i)
			}
			pos += n
		}
	}
	offsets[len(degs)] = int32(pos)
	r.depDegs = degs
	r.depByteOffset = offsets
	return degs, offsets, depsData, nil
}

// ensureTagOffsets builds the per-node byte-offset index into the TAGS column.
func (r *Reader) ensureTagOffsets() ([]int32, []int32, []byte, error) {
	degData, err := r.getColumn(colTagDeg)
	if err != nil {
		return nil, nil, nil, err
	}
	tagsData, err := r.getColumn(colTags)
	if err != nil {
		return nil, nil, nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tagByteOffset != nil {
		return r.tagDegs, r.tagByteOffset, tagsData, nil
	}

	degs := decodePlainInts(degData)
	if err := validateDegs(degs, len(tagsData), "tag"); err != nil {
		return nil, nil, nil, err
	}
	offsets := make([]int32, len(degs)+1)
	pos := 0
	for i, deg := range degs {
		offsets[i] = int32(pos)
		for j := int32(0); j < deg; j++ {
			_, n := readUvarint(tagsData[pos:])
			if n <= 0 {
				return nil, nil, nil, fmt.Errorf("tgb: truncated TAGS column at node %d", i)
			}
			pos += n
		}
	}
	offsets[len(degs)] = int32(pos)
	r.tagDegs = degs
	r.tagByteOffset = offsets
	return degs, offsets, tagsData, nil
}

// ensureAttrOffsets builds the per-node byte-offset indexes into the attribute
// name and value columns. They are indexed separately because the two columns
// hold independently-sized varints for the same logical pairs.
func (r *Reader) ensureAttrOffsets() (degs, nmOff, vlOff []int32, nmData, vlData []byte, err error) {
	degData, err := r.getColumn(colAttrDeg)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	nmData, err = r.getColumn(colAttrName)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	vlData, err = r.getColumn(colAttrValue)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attrNmOffset != nil {
		return r.attrDegs, r.attrNmOffset, r.attrVlOffset, nmData, vlData, nil
	}

	degs = decodePlainInts(degData)
	// Names and values are parallel streams: one entry in each per pair, so
	// both bound the degree total independently.
	if err := validateDegs(degs, len(nmData), "attr-name"); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if err := validateDegs(degs, len(vlData), "attr-value"); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	nmOff = make([]int32, len(degs)+1)
	vlOff = make([]int32, len(degs)+1)
	nmPos, vlPos := 0, 0
	for i, deg := range degs {
		nmOff[i] = int32(nmPos)
		vlOff[i] = int32(vlPos)
		for j := int32(0); j < deg; j++ {
			_, n := readUvarint(nmData[nmPos:])
			if n <= 0 {
				return nil, nil, nil, nil, nil, fmt.Errorf("tgb: truncated ATTR_NAME column at node %d", i)
			}
			nmPos += n
			_, n = readUvarint(vlData[vlPos:])
			if n <= 0 {
				return nil, nil, nil, nil, nil, fmt.Errorf("tgb: truncated ATTR_VALUE column at node %d", i)
			}
			vlPos += n
		}
	}
	nmOff[len(degs)] = int32(nmPos)
	vlOff[len(degs)] = int32(vlPos)
	r.attrDegs = degs
	r.attrNmOffset = nmOff
	r.attrVlOffset = vlOff
	return degs, nmOff, vlOff, nmData, vlData, nil
}

// Deps returns the dependency node indices (in TGB sort order) for node i,
// appended to buf. The first call builds a byte-offset index over the DEPS
// column; subsequent calls are O(degree).
func (r *Reader) Deps(node int, buf []int32) []int32 {
	degs, offsets, depsData, err := r.ensureDepOffsets()
	if err != nil || node >= len(degs) {
		return buf
	}
	pos := int(offsets[node])
	deg := degs[node]
	prev := int32(0)
	for j := int32(0); j < deg; j++ {
		if j == 0 {
			v, n := readZigzag(depsData[pos:])
			pos += n
			d := int32(v) + int32(node)
			buf = append(buf, d)
			prev = d
		} else {
			v, n := readUvarint(depsData[pos:])
			pos += n
			d := prev + int32(v)
			buf = append(buf, d)
			prev = d
		}
	}
	return buf
}

// DepsCSR decodes the entire dependency column in a single sequential pass and
// returns it in compressed-sparse-row form: the dependencies of node i are
// targets[offsets[i]:offsets[i+1]].
//
// This is the shape the comparison's reverse-adjacency build wants. It is
// O(nodes + edges) with two allocations, where calling Deps per node would
// allocate per node and re-walk the offset table.
func (r *Reader) DepsCSR() (offsets []int32, targets []int32, err error) {
	degData, err := r.getColumn(colDeg)
	if err != nil {
		return nil, nil, err
	}
	depsData, err := r.getColumn(colDeps)
	if err != nil {
		return nil, nil, err
	}
	degs := decodePlainInts(degData)
	if err := validateDegs(degs, len(depsData), "dep"); err != nil {
		return nil, nil, err
	}

	total := 0
	for _, d := range degs {
		total += int(d)
	}

	offsets = make([]int32, len(degs)+1)
	targets = make([]int32, 0, total)
	pos := 0
	for i, deg := range degs {
		offsets[i] = int32(len(targets))
		prev := int32(0)
		for j := int32(0); j < deg; j++ {
			if j == 0 {
				v, n := readZigzag(depsData[pos:])
				if n <= 0 {
					return nil, nil, fmt.Errorf("tgb: truncated DEPS column at node %d", i)
				}
				pos += n
				d := int32(v) + int32(i)
				targets = append(targets, d)
				prev = d
			} else {
				v, n := readUvarint(depsData[pos:])
				if n <= 0 {
					return nil, nil, fmt.Errorf("tgb: truncated DEPS column at node %d", i)
				}
				pos += n
				d := prev + int32(v)
				targets = append(targets, d)
				prev = d
			}
		}
	}
	offsets[len(degs)] = int32(len(targets))
	return offsets, targets, nil
}

// Tags returns the tag dict IDs for node i, appended to buf.
func (r *Reader) Tags(node int, buf []int32) []int32 {
	degs, offsets, tagsData, err := r.ensureTagOffsets()
	if err != nil || node >= len(degs) {
		return buf
	}
	pos := int(offsets[node])
	deg := degs[node]
	prev := int32(0)
	for j := int32(0); j < deg; j++ {
		v, n := readUvarint(tagsData[pos:])
		pos += n
		tid := prev + int32(v)
		buf = append(buf, tid)
		prev = tid
	}
	return buf
}

// Attrs returns a map of attr-name → attr-value strings for node i.
func (r *Reader) Attrs(node int) map[string]string {
	degs, nmOff, vlOff, nmData, vlData, err := r.ensureAttrOffsets()
	if err != nil || node >= len(degs) {
		return nil
	}

	r.mu.Lock()
	_ = r.ensureAttrNameDict()
	_ = r.ensureAttrValDict()
	anDict := r.attrNameDict
	avDict := r.attrValDict
	r.mu.Unlock()

	deg := int(degs[node])
	if deg == 0 {
		return map[string]string{}
	}
	nmPos := int(nmOff[node])
	vlPos := int(vlOff[node])
	out := make(map[string]string, deg)
	for j := 0; j < deg; j++ {
		nid, n := readUvarint(nmData[nmPos:])
		nmPos += n
		vid, n := readUvarint(vlData[vlPos:])
		vlPos += n
		var aname, aval string
		if int(nid) < len(anDict) {
			aname = anDict[nid]
		}
		if int(vid) < len(avDict) {
			aval = avDict[vid]
		}
		out[aname] = aval
	}
	return out
}

// Root returns true if node i is a root.
func (r *Reader) Root(node int) bool {
	data, err := r.getColumn(colFlags)
	if err != nil {
		return false
	}
	n := r.NodeCount()
	rootBytes := (n + 7) / 8
	if len(data) < rootBytes {
		return false
	}
	return (data[node/8] & (1 << (uint(node) % 8))) != 0
}

// External returns true if node i is external.
func (r *Reader) External(node int) bool {
	data, err := r.getColumn(colFlags)
	if err != nil {
		return false
	}
	n := r.NodeCount()
	rootBytes := (n + 7) / 8
	extData := data[rootBytes:]
	if node/8 >= len(extData) {
		return false
	}
	return (extData[node/8] & (1 << (uint(node) % 8))) != 0
}

// HashEmpty returns true if node i was encoded with an empty hash string —
// tango's cycle sentinel (Hash = []byte{}). The HASH column holds zeros for
// such nodes; this bit is what distinguishes them from a (theoretical)
// all-zero real hash.
func (r *Reader) HashEmpty(node int) bool {
	_, _, hashEmptyBits, err := r.decodeFlags()
	if err != nil || hashEmptyBits == nil || node/8 >= len(hashEmptyBits) {
		return false
	}
	return (hashEmptyBits[node/8] & (1 << (uint(node) % 8))) != 0
}

// HashEmptyBits returns the raw hash-empty bitset (bit i set means node i
// carried the empty-hash sentinel), or nil when the blob predates the bitset.
// Bulk consumers (comparison) use this instead of per-node HashEmpty calls.
func (r *Reader) HashEmptyBits() ([]byte, error) {
	_, _, hashEmptyBits, err := r.decodeFlags()
	return hashEmptyBits, err
}

// OriginalID returns the entity.OptimizedTarget.ID for node i.
// In TGB v1 (without the optional OriginalID column) the assigned ID is
// simply the node index (see Decode).
func (r *Reader) OriginalID(node int) int32 { return int32(node) }

// ─── internal helpers ─────────────────────────────────────────────────────────

// getColumn returns the decompressed bytes for column id, caching the result.
func (r *Reader) getColumn(id uint64) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getColumnLocked(id)
}

func (r *Reader) getColumnLocked(id uint64) ([]byte, error) {
	if v, ok := r.decompressed[id]; ok {
		return v, nil
	}
	e, ok := r.cols[id]
	if !ok {
		r.decompressed[id] = nil
		return nil, nil
	}
	raw := r.data[e.offset : e.offset+e.compressedSize]
	var out []byte
	var err error
	if e.codec == codecZstd {
		r.DecompressionCount++
		out, err = decompressZstd(raw, e.rawSize)
		if err != nil {
			return nil, fmt.Errorf("tgb: decompress col %d: %w", id, err)
		}
		// Everything downstream sizes loops and allocations off rawSize (via
		// validateColumns); a frame that inflates to a different length than
		// the directory claimed is hostile or corrupt either way.
		if uint64(len(out)) != e.rawSize {
			return nil, fmt.Errorf("tgb: col %d decompressed to %d bytes, directory claims %d",
				id, len(out), e.rawSize)
		}
	} else {
		out = raw
	}
	r.decompressed[id] = out
	return out, nil
}

// decodeStringMap deserializes the format produced by encodeStringMap: count
// (uvarint), then count (key-length, key, value-length, value) pairs.
func decodeStringMap(data []byte) (map[string]string, error) {
	n, sz := binary.Uvarint(data)
	if sz <= 0 {
		return nil, fmt.Errorf("truncated count")
	}
	data = data[sz:]
	m := make(map[string]string, n)
	for i := uint64(0); i < n; i++ {
		kLen, sz := binary.Uvarint(data)
		if sz <= 0 || uint64(len(data)-sz) < kLen {
			return nil, fmt.Errorf("truncated key at entry %d", i)
		}
		data = data[sz:]
		key := string(data[:kLen])
		data = data[kLen:]

		vLen, sz := binary.Uvarint(data)
		if sz <= 0 || uint64(len(data)-sz) < vLen {
			return nil, fmt.Errorf("truncated value at entry %d", i)
		}
		data = data[sz:]
		m[key] = string(data[:vLen])
		data = data[vLen:]
	}
	return m, nil
}

// AllTargetsFileHashes returns the sidecar file-hash map stored in column 22,
// or nil when the column is absent (old blobs or repos without AllTargetsFiles).
// Only the single column is decompressed — no full graph decode.
func (r *Reader) AllTargetsFileHashes() (map[string]string, error) {
	if _, ok := r.cols[colAllTargetsFileHashes]; !ok {
		return nil, nil
	}
	data, err := r.getColumn(colAllTargetsFileHashes)
	if err != nil {
		return nil, err
	}
	return decodeStringMap(data)
}

func (r *Reader) ensurePkgDict() error {
	if r.pkgDict != nil {
		return nil
	}
	data, err := r.getColumnLocked(colPkgDict)
	if err != nil {
		return err
	}
	strs, err := expandDict(data)
	if err != nil {
		return err
	}
	r.pkgDict = strs
	return nil
}

func (r *Reader) ensureNameDict() error {
	if r.nameDict != nil {
		return nil
	}
	data, err := r.getColumnLocked(colNameDict)
	if err != nil {
		return err
	}
	strs, err := expandDict(data)
	if err != nil {
		return err
	}
	r.nameDict = strs
	return nil
}

func (r *Reader) ensureRuleTypeDict() error {
	if r.ruleTypeDict != nil {
		return nil
	}
	data, err := r.getColumnLocked(colRuleTypeDict)
	if err != nil {
		return err
	}
	strs, err := expandDict(data)
	if err != nil {
		return err
	}
	r.ruleTypeDict = strs
	return nil
}

func (r *Reader) ensureTagDict() error {
	if r.tagDict != nil {
		return nil
	}
	data, err := r.getColumnLocked(colTagDict)
	if err != nil {
		return err
	}
	strs, err := expandDict(data)
	if err != nil {
		return err
	}
	r.tagDict = strs
	return nil
}

func (r *Reader) ensureAttrNameDict() error {
	if r.attrNameDict != nil {
		return nil
	}
	data, err := r.getColumnLocked(colAttrNameDict)
	if err != nil {
		return err
	}
	strs, err := expandDict(data)
	if err != nil {
		return err
	}
	r.attrNameDict = strs
	return nil
}

func (r *Reader) ensureAttrValDict() error {
	if r.attrValDict != nil {
		return nil
	}
	data, err := r.getColumnLocked(colAttrValueDict)
	if err != nil {
		return err
	}
	strs, err := expandDict(data)
	if err != nil {
		return err
	}
	r.attrValDict = strs
	return nil
}

func (r *Reader) expandAllDicts() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, fn := range []func() error{
		r.ensurePkgDict, r.ensureNameDict, r.ensureRuleTypeDict,
		r.ensureTagDict, r.ensureAttrNameDict, r.ensureAttrValDict,
	} {
		if err := fn(); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reader) decompressAll() error {
	ids := []uint64{
		colNodePkg, colNodeName, colRuleType, colDeg, colDeps,
		colTagDeg, colTags, colAttrDeg, colAttrName, colAttrValue,
		colFlags, colBlockStart,
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if _, err := r.getColumnLocked(id); err != nil {
			return err
		}
	}
	return nil
}

// getPkgIDColumn returns the decoded pkgID slice, cached after first use.
func (r *Reader) getPkgIDColumn() ([]int32, error) {
	r.mu.Lock()
	if r.pkgIDsCache != nil {
		defer r.mu.Unlock()
		return r.pkgIDsCache, nil
	}
	r.mu.Unlock()

	data, err := r.getColumn(colNodePkg)
	if err != nil {
		return nil, err
	}
	ids := decodeDeltaInts(data)

	r.mu.Lock()
	r.pkgIDsCache = ids
	r.mu.Unlock()
	return ids, nil
}

// getNameIDColumn returns the decoded nameID slice, cached after first use.
func (r *Reader) getNameIDColumn() ([]int32, error) {
	r.mu.Lock()
	if r.nameIDsCache != nil {
		defer r.mu.Unlock()
		return r.nameIDsCache, nil
	}
	r.mu.Unlock()

	pkgIDs, err := r.getPkgIDColumn()
	if err != nil {
		return nil, err
	}
	data, err := r.getColumn(colNodeName)
	if err != nil {
		return nil, err
	}
	ids := decodeNameIDs(data, pkgIDs)

	r.mu.Lock()
	r.nameIDsCache = ids
	r.mu.Unlock()
	return ids, nil
}

// getRuleTypeIDColumn returns the per-node rule-type dictionary IDs, cached.
func (r *Reader) getRuleTypeIDColumn() ([]int32, error) {
	r.mu.Lock()
	if r.ruleTypeIDsCache != nil {
		defer r.mu.Unlock()
		return r.ruleTypeIDsCache, nil
	}
	r.mu.Unlock()

	data, err := r.getColumn(colRuleType)
	if err != nil {
		return nil, err
	}
	ids := decodePlainInts(data)

	r.mu.Lock()
	r.ruleTypeIDsCache = ids
	r.mu.Unlock()
	return ids, nil
}

// RuleTypeIDs exposes the per-node rule-type dictionary IDs so callers can
// test rule types with an integer compare instead of materialising a string
// per node. Do not mutate the returned slice; it is cached and shared.
func (r *Reader) RuleTypeIDs() ([]int32, error) {
	return r.getRuleTypeIDColumn()
}

// RuleTypeDictID returns the dictionary ID for a rule-type string, or -1 if
// the graph contains no target of that rule type. Pair it with RuleTypeIDs to
// test "is this node a source file?" without any string comparison.
func (r *Reader) RuleTypeDictID(name string) int32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureRuleTypeDict(); err != nil {
		return -1
	}
	for i, s := range r.ruleTypeDict {
		if s == name {
			return int32(i)
		}
	}
	return -1
}

// ─── column decoders ──────────────────────────────────────────────────────────

// decodeDeltaInts decodes a uvarint delta-coded sequence.
func decodeDeltaInts(data []byte) []int32 {
	if len(data) == 0 {
		return nil
	}
	var out []int32
	prev := int32(0)
	for len(data) > 0 {
		v, n := binary.Uvarint(data)
		if n <= 0 {
			break
		}
		data = data[n:]
		prev += int32(v)
		out = append(out, prev)
	}
	return out
}

// decodePlainInts decodes a sequence of plain (non-delta) uvarints.
func decodePlainInts(data []byte) []int32 {
	if len(data) == 0 {
		return nil
	}
	var out []int32
	for len(data) > 0 {
		v, n := binary.Uvarint(data)
		if n <= 0 {
			break
		}
		data = data[n:]
		out = append(out, int32(v))
	}
	return out
}

// decodeNameIDs decodes the NODE_NAME column using the pkgID sequence to know
// when packages change (and thus when the nameID resets).
func decodeNameIDs(data []byte, pkgIDs []int32) []int32 {
	if len(data) == 0 {
		return nil
	}
	out := make([]int32, 0, len(pkgIDs))
	prevPkg := int32(-1)
	prevName := int32(0)
	for i := 0; len(data) > 0; i++ {
		v, n := binary.Uvarint(data)
		if n <= 0 {
			break
		}
		data = data[n:]
		var nid int32
		if i < len(pkgIDs) && pkgIDs[i] != prevPkg {
			nid = int32(v) // absolute
			prevPkg = pkgIDs[i]
		} else {
			nid = prevName + int32(v) // delta
		}
		prevName = nid
		out = append(out, nid)
	}
	return out
}

// decodePkgIDs decodes NODE_PKG for the Reader.
func (r *Reader) decodePkgIDs() ([]int32, error) {
	data, err := r.getColumn(colNodePkg)
	if err != nil {
		return nil, err
	}
	return decodeDeltaInts(data), nil
}

// decodeNameIDs decodes NODE_NAME for the Reader.
func (r *Reader) decodeNameIDs(pkgIDs []int32) ([]int32, error) {
	data, err := r.getColumn(colNodeName)
	if err != nil {
		return nil, err
	}
	return decodeNameIDs(data, pkgIDs), nil
}

// decodeRuleTypeIDs decodes RULETYPE.
func (r *Reader) decodeRuleTypeIDs() ([]int32, error) {
	data, err := r.getColumn(colRuleType)
	if err != nil {
		return nil, err
	}
	return decodePlainInts(data), nil
}

// decodeDepColumns returns per-node dep degree and dep node-index slices.
func (r *Reader) decodeDepColumns() (degs []int32, lists [][]int32, err error) {
	degData, err := r.getColumn(colDeg)
	if err != nil {
		return nil, nil, err
	}
	depsData, err := r.getColumn(colDeps)
	if err != nil {
		return nil, nil, err
	}
	degs = decodePlainInts(degData)
	if err := validateDegs(degs, len(depsData), "dep"); err != nil {
		return nil, nil, err
	}
	lists = make([][]int32, len(degs))
	pos := 0
	for i, deg := range degs {
		if deg == 0 {
			lists[i] = nil
			continue
		}
		lst := make([]int32, deg)
		prev := int32(0)
		for j := int32(0); j < deg; j++ {
			if j == 0 {
				v, n := readZigzag(depsData[pos:])
				if n <= 0 {
					return nil, nil, fmt.Errorf("tgb: truncated DEPS column at node %d", i)
				}
				pos += n
				lst[j] = int32(v) + int32(i)
				prev = lst[j]
			} else {
				v, n := readUvarint(depsData[pos:])
				if n <= 0 {
					return nil, nil, fmt.Errorf("tgb: truncated DEPS column at node %d", i)
				}
				pos += n
				lst[j] = prev + int32(v)
				prev = lst[j]
			}
		}
		lists[i] = lst
	}
	return degs, lists, nil
}

// decodeTagColumns returns per-node tag degrees and tag dict-ID slices.
func (r *Reader) decodeTagColumns() (degs []int32, lists [][]int32, err error) {
	degData, err := r.getColumn(colTagDeg)
	if err != nil {
		return nil, nil, err
	}
	tagsData, err := r.getColumn(colTags)
	if err != nil {
		return nil, nil, err
	}
	degs = decodePlainInts(degData)
	if err := validateDegs(degs, len(tagsData), "tag"); err != nil {
		return nil, nil, err
	}
	lists = make([][]int32, len(degs))
	pos := 0
	for i, deg := range degs {
		if deg == 0 {
			lists[i] = nil
			continue
		}
		lst := make([]int32, deg)
		prev := int32(0)
		for j := int32(0); j < deg; j++ {
			v, n := readUvarint(tagsData[pos:])
			if n <= 0 {
				return nil, nil, fmt.Errorf("tgb: truncated TAGS column at node %d", i)
			}
			pos += n
			lst[j] = prev + int32(v)
			prev = lst[j]
		}
		lists[i] = lst
	}
	return degs, lists, nil
}

// decodeAttrColumns returns per-node attr degrees, name IDs, and value IDs.
func (r *Reader) decodeAttrColumns() (degs []int32, names [][]int32, vals [][]int32, err error) {
	degData, err := r.getColumn(colAttrDeg)
	if err != nil {
		return nil, nil, nil, err
	}
	nmData, err := r.getColumn(colAttrName)
	if err != nil {
		return nil, nil, nil, err
	}
	vlData, err := r.getColumn(colAttrValue)
	if err != nil {
		return nil, nil, nil, err
	}
	degs = decodePlainInts(degData)
	if err := validateDegs(degs, len(nmData), "attr-name"); err != nil {
		return nil, nil, nil, err
	}
	if err := validateDegs(degs, len(vlData), "attr-value"); err != nil {
		return nil, nil, nil, err
	}
	names = make([][]int32, len(degs))
	vals = make([][]int32, len(degs))
	nmPos := 0
	vlPos := 0
	for i, deg := range degs {
		if deg == 0 {
			names[i] = nil
			vals[i] = nil
			continue
		}
		nms := make([]int32, deg)
		vls := make([]int32, deg)
		for j := int32(0); j < deg; j++ {
			nv, n := readUvarint(nmData[nmPos:])
			if n <= 0 {
				return nil, nil, nil, fmt.Errorf("tgb: truncated ATTR_NAME column at node %d", i)
			}
			nmPos += n
			nms[j] = int32(nv)
			vv, n := readUvarint(vlData[vlPos:])
			if n <= 0 {
				return nil, nil, nil, fmt.Errorf("tgb: truncated ATTR_VALUE column at node %d", i)
			}
			vlPos += n
			vls[j] = int32(vv)
		}
		names[i] = nms
		vals[i] = vls
	}
	return degs, names, vals, nil
}

// decodeFlags returns the root and external bitsets.
func (r *Reader) decodeFlags() (rootBits, extBits, hashEmptyBits []byte, err error) {
	data, err := r.getColumn(colFlags)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(data) == 0 {
		return nil, nil, nil, nil
	}
	n := r.NodeCount()
	nb := (n + 7) / 8
	if len(data) < nb {
		return data, nil, nil, nil
	}
	if len(data) < 2*nb {
		return data[:nb], data[nb:], nil, nil
	}
	if len(data) < 3*nb {
		return data[:nb], data[nb : 2*nb], nil, nil
	}
	return data[:nb], data[nb : 2*nb], data[2*nb : 3*nb], nil
}

// ─── ColumnStats ─────────────────────────────────────────────────────────────

// ColumnStat holds size information for a single column.
type ColumnStat struct {
	ID             uint64
	Name           string
	RawSize        uint64
	CompressedSize uint64
}

// ColumnStats returns per-column size statistics from an encoded TGB blob.
func ColumnStats(data []byte) ([]ColumnStat, error) {
	r, err := NewReader(data)
	if err != nil {
		return nil, err
	}
	stats := make([]ColumnStat, 0, len(r.cols))
	for id, e := range r.cols {
		name := colNames[id]
		if name == "" {
			name = fmt.Sprintf("col%d", id)
		}
		stats = append(stats, ColumnStat{
			ID:             id,
			Name:           name,
			RawSize:        e.rawSize,
			CompressedSize: e.compressedSize,
		})
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].ID < stats[j].ID })
	return stats, nil
}
