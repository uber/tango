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

// Package tgbdiff implements the TGB-native graph comparison algorithm.
// It diffs two encoded target graphs (represented as *tgb.Reader) without
// ever materialising map[string]*Target, and classifies changes with exactly
// the same rules as internal/targetdiff — targetdiff is the semantic
// reference, and the differential tests in this package hold the two
// implementations to identical output.
//
// The algorithm runs in four phases:
//
//   - Phase 0 (Align): Walk both BLOCK_DIGEST arrays. Equal digests mean the
//     two blocks hold the same labels in the same order; pair those without
//     decoding anything. For non-matching digests, decode labels and merge-join
//     locally to find aligned runs.
//
//   - Phase 1 (HashDiff): For each aligned run, one bytes.Equal over the
//     HASH column. Where runs differ, bisect recursively to isolate individual
//     changed entries in O(k log(n/k)) comparisons. Every aligned run is
//     checked here, including ones phase 0 paired by digest.
//
//   - Phase 2 (SeedClassify): Decode deps/tags/attrs only for changed nodes.
//     Classification mirrors targetdiff exactly, including the !anyChanged ||
//     depsChanged condition.
//
//   - Phase 3 (ReverseCSR + BFS): Build reverse adjacency as a CSR in three
//     linear passes, then run multi-source BFS with a visited bitset and an
//     int32 distance array; no maps, no allocation in the loop.
package tgbdiff

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/uber/tango/internal/targetdiff"
	"github.com/uber/tango/internal/tgb"
)

// sourceFileRuleType is the rule type string bazel reports for source files.
// It must match targetdiff's unexported _sourceFileRuleType.
const sourceFileRuleType = "source file"

// ChangedTarget is one entry of a comparison result, keyed by canonical label
// so that results from different implementations can be compared directly.
// Materialize (adapt.go) turns these into full targetdiff.ChangedTargets.
type ChangedTarget struct {
	Label      string
	ChangeType targetdiff.ChangeType
	Distance   int32
	Seed       bool
}

// Result is the output of a comparison, sorted by Label.
type Result struct {
	Changed []ChangedTarget
}

// Options controls comparison behaviour.
type Options struct {
	// MaxDistance limits reverse-dependency BFS. -1 means no limit.
	MaxDistance int32

	// SeedAttrs, when non-nil, restricts which attribute names are considered
	// when classifying an attribute-only change as a seed. It mirrors the
	// RepositoryConfig.SeedAttributes allowlist that the incumbent path
	// applies in toDiffGraph before comparing.
	SeedAttrs map[string]bool
}

// Phases holds per-phase wall-clock durations for a single CompareInstrumented call.
type Phases struct {
	Align        time.Duration // phase 0: block-digest skip + local merge-join
	HashDiff     time.Duration // phase 1: bulk memcmp over aligned runs
	SeedClassify time.Duration // phase 2: lazy decode of changed nodes only
	ReverseCSR   time.Duration // phase 3a: build reverse adjacency
	BFS          time.Duration // phase 3b: multi-source BFS
}

// Counters holds diagnostic counters for a single comparison.
type Counters struct {
	BlocksSkipped     int // block digests that matched, never decoded
	BlocksDecoded     int // blocks that needed label decoding (didn't skip)
	NodesLabelDecoded int // nodes whose label was actually materialised
	HashBytesCompared int // total hash bytes touched in phase 1
	ChangedNodes      int // nodes identified as changed (before/after classification)
	Seeds             int // nodes classified as seeds
}

// ─── internal types ───────────────────────────────────────────────────────────

// alignedRun is a maximal run of nodes present in both graphs at the same
// relative offset with the same label.
type alignedRun struct {
	beforeStart int
	afterStart  int
	length      int
}

type changeKind uint8

const (
	changeKindNew     changeKind = iota
	changeKindChanged changeKind = iota
	changeKindDeleted changeKind = iota
)

// changedNode tracks a single changed target across all phases.
type changedNode struct {
	label       string
	kind        changeKind
	distance    int32
	seed        bool
	afterIndex  int32 // TGB node index in after graph; -1 for deleted
	beforeIndex int32 // TGB node index in before graph; -1 for new
}

// ─── public API ───────────────────────────────────────────────────────────────

// Compare classifies changes between before and after encoded graphs and
// computes reverse-dependency distances from seed targets. The result is
// sorted by Label for deterministic comparison with reflect.DeepEqual.
func Compare(ctx context.Context, before, after *tgb.Reader, opts Options) (*Result, error) {
	result, _, _, err := compareInternal(ctx, before, after, opts)
	return result, err
}

// CompareInstrumented is like Compare but also returns per-phase timings and
// diagnostic counters.
func CompareInstrumented(ctx context.Context, before, after *tgb.Reader, opts Options) (*Result, Phases, Counters, error) {
	return compareInternal(ctx, before, after, opts)
}

// ─── main entry ───────────────────────────────────────────────────────────────

func compareInternal(ctx context.Context, before, after *tgb.Reader, opts Options) (*Result, Phases, Counters, error) {
	var phases Phases
	var cnt Counters

	if err := ctx.Err(); err != nil {
		return nil, phases, cnt, context.Cause(ctx)
	}

	// ── Phase 0: align ───────────────────────────────────────────────────────
	t0 := time.Now()
	runs, deletedBefore, insertedAfter, err := align(before, after, &cnt)
	if err != nil {
		return nil, phases, cnt, err
	}
	phases.Align = time.Since(t0)

	if err := ctx.Err(); err != nil {
		return nil, phases, cnt, context.Cause(ctx)
	}

	// ── Phase 1: bulk hash diff ───────────────────────────────────────────────
	t1 := time.Now()

	bHashes := before.Hashes()
	aHashes := after.Hashes()
	hashStride := before.HashBytes()
	// Phase 1 compares the two HASH columns positionally, so the strides must
	// agree; comparing an 8-byte blob against a 20-byte blob would misalign
	// every entry after the first.
	if after.HashBytes() != hashStride {
		return nil, phases, cnt, fmt.Errorf(
			"tgbdiff: hash stride mismatch: before=%d after=%d bytes",
			hashStride, after.HashBytes())
	}

	nAfterNodes := after.NodeCount()
	nBeforeNodes := before.NodeCount()

	// Changed nodes are tracked in node-index space rather than by label.
	// Labels are materialised once, at result assembly, because resolving a
	// node index to a label allocates a string and phase 2 would otherwise do
	// that once per dependency of every changed node.
	//
	// changedByAfter is indexed by after-node index; deletedNodes holds nodes
	// present only in the before graph, which have no after index.
	changedByAfter := make([]*changedNode, nAfterNodes)
	var deletedNodes []*changedNode

	// beforeToAfter maps a before-node index to the matching after-node index,
	// or -1 when that label is absent from the after graph. Phase 0's aligned
	// runs already establish this correspondence for free.
	beforeToAfter := make([]int32, nBeforeNodes)
	for i := range beforeToAfter {
		beforeToAfter[i] = -1
	}
	for _, run := range runs {
		for k := 0; k < run.length; k++ {
			beforeToAfter[run.beforeStart+k] = int32(run.afterStart + k)
		}
	}

	// Deleted nodes: in before, not in after.
	for _, bi := range deletedBefore {
		deletedNodes = append(deletedNodes, &changedNode{
			kind:        changeKindDeleted,
			afterIndex:  -1,
			beforeIndex: int32(bi),
		})
	}

	// New nodes: in after, not in before.
	for _, ai := range insertedAfter {
		changedByAfter[ai] = &changedNode{
			kind:        changeKindNew,
			afterIndex:  int32(ai),
			beforeIndex: -1,
		}
	}

	// Phase 1: for each aligned run compare HASH slices, bisect where different.
	for _, run := range runs {
		bSlice := bHashes[run.beforeStart*hashStride : (run.beforeStart+run.length)*hashStride]
		aSlice := aHashes[run.afterStart*hashStride : (run.afterStart+run.length)*hashStride]
		cnt.HashBytesCompared += len(bSlice)

		if bytes.Equal(bSlice, aSlice) {
			continue // entire run unchanged
		}
		bisectHashRun(bHashes, aHashes, hashStride,
			run.beforeStart, run.afterStart, run.length,
			changedByAfter, &cnt,
		)
	}

	// The empty-hash cycle sentinel ("") encodes as zero bytes in the HASH
	// column plus a bit in the hash-empty bitset — byte-identical to a real
	// all-zero digest. The byte comparison above therefore cannot see a node
	// flip between "" and "000…0"; the bitsets break the tie. (targetdiff
	// compares hash strings, where "" != "000…0".)
	bEmptyBits, err := before.HashEmptyBits()
	if err != nil {
		return nil, phases, cnt, err
	}
	aEmptyBits, err := after.HashEmptyBits()
	if err != nil {
		return nil, phases, cnt, err
	}
	if bEmptyBits != nil || aEmptyBits != nil {
		bit := func(bits []byte, i int) bool {
			return bits != nil && i/8 < len(bits) && bits[i/8]&(1<<(uint(i)%8)) != 0
		}
		for _, run := range runs {
			for k := 0; k < run.length; k++ {
				ai := run.afterStart + k
				if changedByAfter[ai] != nil {
					continue
				}
				if bit(bEmptyBits, run.beforeStart+k) != bit(aEmptyBits, ai) {
					changedByAfter[ai] = &changedNode{
						kind:        changeKindChanged,
						afterIndex:  int32(ai),
						beforeIndex: int32(run.beforeStart + k),
					}
				}
			}
		}
	}

	// One stable iteration order for every later phase.
	changedList := make([]*changedNode, 0, len(deletedNodes)+len(insertedAfter)+32)
	changedList = append(changedList, deletedNodes...)
	for _, cn := range changedByAfter {
		if cn != nil {
			changedList = append(changedList, cn)
		}
	}

	cnt.ChangedNodes = len(changedList)
	phases.HashDiff = time.Since(t1)

	if err := ctx.Err(); err != nil {
		return nil, phases, cnt, context.Cause(ctx)
	}

	// ── Phase 2: seed classification ─────────────────────────────────────────
	t2 := time.Now()

	// Rule types are tested by dictionary ID. RuleType(node) materialises a
	// string, and this test runs once per dependency of every changed node.
	afterRuleTypeIDs, rtErr := after.RuleTypeIDs()
	if rtErr != nil {
		return nil, phases, cnt, rtErr
	}
	srcFileTypeID := after.RuleTypeDictID(sourceFileRuleType)

	// Pass 1 of targetdiff: new and deleted nodes are seeds; a changed source file
	// is a seed.
	for _, node := range changedList {
		if node.kind == changeKindNew || node.kind == changeKindDeleted {
			node.seed = true
			continue
		}
		if node.afterIndex >= 0 && srcFileTypeID >= 0 &&
			int(node.afterIndex) < len(afterRuleTypeIDs) &&
			afterRuleTypeIDs[node.afterIndex] == srcFileTypeID {
			node.seed = true
		}
	}

	// Seed classification loop (mirrors targetdiff.Compare's seed-classify
	// loop), operating on node indices throughout.
	var afterDepBuf, beforeDepBuf, mappedBuf []int32
	afterDepSet := make(map[int32]struct{}, 64)
	for _, node := range changedList {
		if node.seed || node.kind != changeKindChanged {
			continue
		}

		afterDepBuf = after.Deps(int(node.afterIndex), afterDepBuf[:0])
		beforeDepBuf = before.Deps(int(node.beforeIndex), beforeDepBuf[:0])

		// Project before-deps into after-index space. -1 means that
		// dependency's label is absent from the after graph entirely, which is
		// exactly the case the label-based version detected as a set change.
		mappedBuf = mappedBuf[:0]
		for _, bd := range beforeDepBuf {
			if int(bd) < len(beforeToAfter) {
				mappedBuf = append(mappedBuf, beforeToAfter[bd])
			} else {
				mappedBuf = append(mappedBuf, -1)
			}
		}

		anyChanged, depsChanged := changedDepStatusIdx(mappedBuf, afterDepBuf, changedByAfter, afterDepSet)

		// targetdiff: if !anyChanged || depsChanged → seed.
		if !anyChanged || depsChanged {
			node.seed = true
			continue
		}

		if hasChangedSrcFileDepIdx(afterDepBuf, changedByAfter, afterRuleTypeIDs, srcFileTypeID) {
			node.seed = true
			continue
		}

		afterAttrs := decodeAttrs(after, int(node.afterIndex), opts.SeedAttrs)
		beforeAttrs := decodeAttrs(before, int(node.beforeIndex), opts.SeedAttrs)
		if attrsChanged(beforeAttrs, afterAttrs) {
			node.seed = true
		}
	}

	seedCount := 0
	for _, node := range changedList {
		if node.seed {
			seedCount++
		}
	}
	cnt.Seeds = seedCount
	phases.SeedClassify = time.Since(t2)

	if err := ctx.Err(); err != nil {
		return nil, phases, cnt, context.Cause(ctx)
	}

	// ── Phase 3a: build reverse CSR ──────────────────────────────────────────
	t3 := time.Now()
	nAfter := nAfterNodes
	var csrDepBuf []int32
	csrOffsets, csrTargets := buildReverseCSR(after, nAfter, &csrDepBuf)
	phases.ReverseCSR = time.Since(t3)

	// ── Phase 3b: BFS ────────────────────────────────────────────────────────
	t4 := time.Now()

	// Initialise: seeds get distance 0; non-seeds get distance -1.
	for _, node := range changedList {
		if node.seed {
			node.distance = 0
		} else {
			node.distance = -1
		}
	}

	// Visited bitset over [0, nAfter).
	visited := make([]uint64, (nAfter+63)/64)
	distArr := make([]int32, nAfter)
	for i := range distArr {
		distArr[i] = -1
	}

	// Seed the BFS queue with all seeds that exist in the after graph.
	queue := make([]int32, 0, seedCount)
	for _, node := range changedList {
		if node.seed && node.afterIndex >= 0 {
			ai := node.afterIndex
			if visited[ai/64]&(1<<(uint(ai)%64)) == 0 {
				visited[ai/64] |= 1 << (uint(ai) % 64)
				distArr[ai] = 0
				queue = append(queue, ai)
			}
		}
	}

	// BFS loop — no maps, no allocations in the loop.
	for head := 0; head < len(queue); head++ {
		if head%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, phases, cnt, context.Cause(ctx)
			}
		}
		cur := queue[head]
		curDist := distArr[cur]

		// Reverse neighbours of cur in CSR.
		lo := csrOffsets[cur]
		hi := csrOffsets[cur+1]
		for _, nb := range csrTargets[lo:hi] {
			if visited[nb/64]&(1<<(uint(nb)%64)) != 0 {
				continue
			}
			newDist := curDist + 1
			if opts.MaxDistance >= 0 && newDist > opts.MaxDistance {
				continue
			}
			visited[nb/64] |= 1 << (uint(nb) % 64)
			distArr[nb] = newDist
			queue = append(queue, nb)

			// If nb corresponds to a changed node, update its distance.
			if entry := changedByAfter[nb]; entry != nil {
				entry.distance = newDist
			}
		}
	}
	phases.BFS = time.Since(t4)

	// ── Build sorted result ───────────────────────────────────────────────────
	// Labels are resolved here and nowhere else: exactly one per changed node.
	changed := make([]ChangedTarget, 0, len(changedList))
	for _, node := range changedList {
		ct := targetdiff.ChangeTypeChanged
		switch node.kind {
		case changeKindNew:
			ct = targetdiff.ChangeTypeNew
		case changeKindDeleted:
			ct = targetdiff.ChangeTypeDeleted
		}
		label := node.label
		if label == "" {
			if node.afterIndex >= 0 {
				label = after.Label(int(node.afterIndex))
			} else {
				label = before.Label(int(node.beforeIndex))
			}
			cnt.NodesLabelDecoded++
		}
		changed = append(changed, ChangedTarget{
			Label:      label,
			ChangeType: ct,
			Distance:   node.distance,
			Seed:       node.seed,
		})
	}
	sort.Slice(changed, func(i, j int) bool {
		return changed[i].Label < changed[j].Label
	})

	return &Result{Changed: changed}, phases, cnt, nil
}

// ─── phase 0: align ──────────────────────────────────────────────────────────

// align walks both BLOCK_DIGEST arrays and produces:
//   - runs: maximal aligned runs of (beforeStart, afterStart, length)
//   - deletedBefore: before-node indices absent from after
//   - insertedAfter: after-node indices absent from before
//
// A block digest is a hash over the block's label bytes, so equal digests mean
// "these two blocks hold the same labels in the same order" — nothing more.
// That is exactly what alignment needs. Those blocks are paired without
// decoding a single label, and their nodes go into an aligned run.
//
// Equal digest does NOT mean equal content, and matching a block here does NOT
// exempt it from phase 1: every aligned run is hash-compared regardless. A
// block whose deps or attributes changed still has a different Merkle hash, and
// phase 1 is what catches it.
//
// For non-matching digests, labels are decoded and merge-joined locally to
// find which nodes are shared and which are deleted/inserted.
func align(before, after *tgb.Reader, cnt *Counters) (
	runs []alignedRun, deletedBefore []int, insertedAfter []int, err error,
) {
	bN := before.NodeCount()
	aN := after.NodeCount()

	bBlockCount := before.BlockCount()
	aBlockCount := after.BlockCount()

	// Edge case: no block-digest column on either side — fall back to full
	// label merge-join.
	if bBlockCount == 0 && aBlockCount == 0 {
		if bN > 0 || aN > 0 {
			return alignFallback(before, after, bN, aN, cnt)
		}
		return nil, nil, nil, nil
	}
	if bBlockCount == 0 {
		for ni := 0; ni < aN; ni++ {
			insertedAfter = append(insertedAfter, ni)
			cnt.NodesLabelDecoded++
		}
		return nil, nil, insertedAfter, nil
	}
	if aBlockCount == 0 {
		for ni := 0; ni < bN; ni++ {
			deletedBefore = append(deletedBefore, ni)
			cnt.NodesLabelDecoded++
		}
		return nil, deletedBefore, nil, nil
	}

	bDigests := before.BlockDigests()
	aDigests := after.BlockDigests()

	// Precompute block-start arrays.
	bBlockStarts := make([]int, bBlockCount+1)
	for i := 0; i < bBlockCount; i++ {
		bBlockStarts[i] = before.BlockStart(i)
	}
	bBlockStarts[bBlockCount] = bN

	aBlockStarts := make([]int, aBlockCount+1)
	for i := 0; i < aBlockCount; i++ {
		aBlockStarts[i] = after.BlockStart(i)
	}
	aBlockStarts[aBlockCount] = aN

	// Build after-digest map: digest → ordered list of (blockIdx, start, end).
	type blockRange struct {
		blockIdx int
		start    int
		end      int
	}
	aDigestMap := make(map[uint64][]blockRange, len(aDigests))
	for i, d := range aDigests {
		aDigestMap[d] = append(aDigestMap[d], blockRange{i, aBlockStarts[i], aBlockStarts[i+1]})
	}

	aQueuePtr := make(map[uint64]int, len(aDigestMap))
	aMatched := make([]bool, aBlockCount)
	afterPos := 0 // minimum acceptable aStart for the next match

	type matchedPair struct {
		bStart, bEnd int
		aStart, aEnd int
		digestMatch  bool // true = paired by block digest rather than merge-join
	}

	var pairs []matchedPair

	for bi := 0; bi < bBlockCount; bi++ {
		bStart := bBlockStarts[bi]
		bEnd := bBlockStarts[bi+1]
		bLen := bEnd - bStart
		d := bDigests[bi]

		queue, hasQueue := aDigestMap[d]
		if !hasQueue {
			cnt.BlocksDecoded++
			continue
		}

		ptr := aQueuePtr[d]
		found := false
		for ptr < len(queue) {
			br := queue[ptr]
			aLen := br.end - br.start
			ptr++
			if br.start < afterPos {
				continue
			}
			if aLen != bLen {
				// Same digest but different length — collision; treat as unmatched.
				continue
			}
			aMatched[br.blockIdx] = true
			pairs = append(pairs, matchedPair{bStart, bEnd, br.start, br.end, true})
			afterPos = br.end
			cnt.BlocksSkipped++
			found = true
			break
		}
		aQueuePtr[d] = ptr

		if !found {
			cnt.BlocksDecoded++
		}
	}

	// Merge-join the unmatched regions, one gap at a time.
	//
	// The digest-matched pairs are order-preserving on both sides: the loop
	// above walks before-blocks in order, and afterPos forces each match's
	// aStart to lie at or beyond the previous match's aEnd. They therefore cut
	// both node sequences into the same number of gaps, and gap k on the
	// before side corresponds to gap k on the after side.
	//
	// A label in a gap on one side can only appear in the *same* gap on the
	// other: matched blocks hold identical label sequences, both graphs are
	// globally sorted by (pkg, name), so a label strictly between two matched
	// runs sorts strictly between them in both graphs. Joining gap-by-gap is
	// exactly equivalent to one global join over all unmatched nodes, but does
	// the work in place — no pooled index slices proportional to the total
	// unmatched region.
	//
	// The merge key is the (pkg, name) tuple, NOT the joined label: '/' sorts
	// below ':', so for nested packages ("//a", "t") < ("//a/b", "t") while
	// "//a/b:t" < "//a:t". Comparing joined labels would walk the two sides in
	// an order neither is sorted in and mispair them.
	for ai := 0; ai < aBlockCount; ai++ {
		if !aMatched[ai] {
			cnt.BlocksDecoded++
		}
	}

	mergeGap := func(gb0, gb1, ga0, ga1 int) {
		bi2, ai2 := gb0, ga0
		for bi2 < gb1 && ai2 < ga1 {
			bPkg, bName := before.SplitLabel(bi2)
			aPkg, aName := after.SplitLabel(ai2)
			cnt.NodesLabelDecoded += 2
			switch {
			case bPkg == aPkg && bName == aName:
				// Same label in both — a length-1 aligned run.
				pairs = append(pairs, matchedPair{bi2, bi2 + 1, ai2, ai2 + 1, false})
				bi2++
				ai2++
			case bPkg < aPkg || (bPkg == aPkg && bName < aName):
				deletedBefore = append(deletedBefore, bi2)
				bi2++
			default:
				insertedAfter = append(insertedAfter, ai2)
				ai2++
			}
		}
		for ; bi2 < gb1; bi2++ {
			deletedBefore = append(deletedBefore, bi2)
			cnt.NodesLabelDecoded++
		}
		for ; ai2 < ga1; ai2++ {
			insertedAfter = append(insertedAfter, ai2)
			cnt.NodesLabelDecoded++
		}
	}

	// pairs currently holds only digest matches, already ascending on both
	// sides; walk them to enumerate the gaps. mergeGap appends to pairs, so
	// iterate over a snapshot.
	matched := make([]matchedPair, len(pairs))
	copy(matched, pairs)
	prevB, prevA := 0, 0
	for _, p := range matched {
		mergeGap(prevB, p.bStart, prevA, p.aStart)
		prevB, prevA = p.bEnd, p.aEnd
	}
	mergeGap(prevB, bN, prevA, aN)

	// Sort all pairs by bStart and merge adjacent ones into maximal runs.
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].bStart < pairs[j].bStart })

	for i := 0; i < len(pairs); {
		run := alignedRun{
			beforeStart: pairs[i].bStart,
			afterStart:  pairs[i].aStart,
			length:      pairs[i].bEnd - pairs[i].bStart,
		}
		j := i + 1
		for j < len(pairs) {
			prev := pairs[j-1]
			cur := pairs[j]
			if cur.bStart == prev.bEnd && cur.aStart == prev.aEnd {
				run.length += cur.bEnd - cur.bStart
				j++
			} else {
				break
			}
		}
		runs = append(runs, run)
		i = j
	}

	return runs, deletedBefore, insertedAfter, nil
}

// alignFallback handles the case where block digests are absent. It falls back
// to decoding labels for all nodes on both sides and doing a full merge-join.
// This is O(n) in label decodes and is only used when BLOCK_DIGEST is missing.
func alignFallback(before, after *tgb.Reader, bN, aN int, cnt *Counters) (
	runs []alignedRun, deletedBefore []int, insertedAfter []int, err error,
) {
	// Decode all (pkg, name) pairs on both sides. See the note in align: the
	// merge key must be the (pkg, name) tuple the encoder sorted on, not the
	// joined label.
	type labelKey struct{ pkg, name string }
	bLabels := make([]labelKey, bN)
	for i := 0; i < bN; i++ {
		p, n := before.SplitLabel(i)
		bLabels[i] = labelKey{p, n}
		cnt.NodesLabelDecoded++
	}
	aLabels := make([]labelKey, aN)
	for i := 0; i < aN; i++ {
		p, n := after.SplitLabel(i)
		aLabels[i] = labelKey{p, n}
		cnt.NodesLabelDecoded++
	}

	// Both sides are sorted by (pkg, name), so do a merge-join.
	bi, ai := 0, 0
	for bi < bN && ai < aN {
		bl, al := bLabels[bi], aLabels[ai]
		switch {
		case bl == al:
			// Extend or start a run.
			if len(runs) > 0 {
				last := &runs[len(runs)-1]
				if last.beforeStart+last.length == bi && last.afterStart+last.length == ai {
					last.length++
					bi++
					ai++
					continue
				}
			}
			runs = append(runs, alignedRun{bi, ai, 1})
			bi++
			ai++
		case bl.pkg < al.pkg || (bl.pkg == al.pkg && bl.name < al.name):
			deletedBefore = append(deletedBefore, bi)
			bi++
		default:
			insertedAfter = append(insertedAfter, ai)
			ai++
		}
	}
	for ; bi < bN; bi++ {
		deletedBefore = append(deletedBefore, bi)
	}
	for ; ai < aN; ai++ {
		insertedAfter = append(insertedAfter, ai)
	}
	return runs, deletedBefore, insertedAfter, nil
}

// ─── phase 1: bisect ─────────────────────────────────────────────────────────

// bisectHashRun recursively bisects a run where the hash slices differ,
// isolating individual changed entries in O(k log(n/k)) comparisons.
func bisectHashRun(
	bHashes, aHashes []byte,
	stride int,
	bStart, aStart, length int,
	changedByAfter []*changedNode,
	cnt *Counters,
) {
	if length == 0 {
		return
	}
	if length == 1 {
		// Single entry — record as Changed. No label is resolved here; the
		// after index identifies the node, and alignment guarantees the before
		// index holds the same label.
		if changedByAfter[aStart] == nil {
			changedByAfter[aStart] = &changedNode{
				kind:        changeKindChanged,
				afterIndex:  int32(aStart),
				beforeIndex: int32(bStart),
			}
		}
		return
	}

	half := length / 2

	bLeft := bHashes[bStart*stride : (bStart+half)*stride]
	aLeft := aHashes[aStart*stride : (aStart+half)*stride]
	cnt.HashBytesCompared += len(bLeft)
	if !bytes.Equal(bLeft, aLeft) {
		bisectHashRun(bHashes, aHashes, stride, bStart, aStart, half, changedByAfter, cnt)
	}

	bRight := bHashes[(bStart+half)*stride : (bStart+length)*stride]
	aRight := aHashes[(aStart+half)*stride : (aStart+length)*stride]
	cnt.HashBytesCompared += len(bRight)
	if !bytes.Equal(bRight, aRight) {
		bisectHashRun(bHashes, aHashes, stride, bStart+half, aStart+half, length-half, changedByAfter, cnt)
	}
}

// ─── phase 2 helpers ─────────────────────────────────────────────────────────

// changedDepStatus mirrors targetdiff.changedDependencyStatus exactly.
// Returns (anyChanged, setChanged).
//
// anyChanged: at least one after-dep is in changedByLabel with kind=Changed.
// setChanged: the dep set changed shape (different count, or before has dep not in after).
// changedDepStatusIdx is changedDependencyStatus from targetdiff, in node-index
// space.
//
// beforeDeps holds the before-graph dependencies already projected into
// after-index space, where -1 marks a dependency whose label does not exist in
// the after graph. Because label-to-index is a bijection within each graph and
// the projection maps equal labels to equal indices, comparing indices here is
// equivalent to comparing label strings.
//
// depSet is caller-owned scratch, cleared on entry, so the hot loop does not
// allocate a map per changed node.
func changedDepStatusIdx(
	beforeDeps, afterDeps []int32,
	changedByAfter []*changedNode,
	depSet map[int32]struct{},
) (anyChanged, setChanged bool) {
	lengthsMatch := len(beforeDeps) == len(afterDeps)

	if lengthsMatch && len(afterDeps) > 0 {
		clear(depSet)
	}
	for _, di := range afterDeps {
		if di >= 0 && int(di) < len(changedByAfter) {
			if e := changedByAfter[di]; e != nil && e.kind == changeKindChanged {
				anyChanged = true
			}
		}
		if lengthsMatch && len(afterDeps) > 0 {
			depSet[di] = struct{}{}
		}
	}
	if !lengthsMatch {
		return anyChanged, true
	}
	for _, di := range beforeDeps {
		if di < 0 {
			// Dependency absent from the after graph entirely.
			return anyChanged, true
		}
		if _, exists := depSet[di]; !exists {
			return anyChanged, true
		}
	}
	return anyChanged, false
}

// hasChangedSrcFileDepIdx is hasChangedSourceFileDependency from targetdiff, in
// node-index space, testing rule type by dictionary ID rather than string.
func hasChangedSrcFileDepIdx(
	afterDeps []int32,
	changedByAfter []*changedNode,
	afterRuleTypeIDs []int32,
	srcFileTypeID int32,
) bool {
	if srcFileTypeID < 0 {
		return false
	}
	for _, di := range afterDeps {
		if di < 0 || int(di) >= len(changedByAfter) {
			continue
		}
		if changedByAfter[di] == nil {
			continue
		}
		if int(di) < len(afterRuleTypeIDs) && afterRuleTypeIDs[di] == srcFileTypeID {
			return true
		}
	}
	return false
}

// decodeAttrs decodes the string attributes for node from r, optionally
// filtering to seedAttrs. Mirrors the controller's toDiffGraph attribute handling.
func decodeAttrs(r *tgb.Reader, node int, seedAttrs map[string]bool) map[string]string {
	raw := r.Attrs(node)
	if len(raw) == 0 {
		return map[string]string{}
	}
	if seedAttrs == nil {
		return raw
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if seedAttrs[k] {
			out[k] = v
		}
	}
	return out
}

// attrsChanged mirrors targetdiff.attributesChanged.
func attrsChanged(before, after map[string]string) bool {
	if len(before) != len(after) {
		return true
	}
	for name, value := range before {
		if afterValue, exists := after[name]; !exists || afterValue != value {
			return true
		}
	}
	return false
}

// ─── phase 3a: reverse CSR ───────────────────────────────────────────────────

// buildReverseCSR builds the reverse adjacency of the after graph as a CSR
// in three linear passes (count, prefix-sum, scatter).
//
// Returns (offsets, targets) where reverse neighbours of node i are
// targets[offsets[i]:offsets[i+1]].
//
// depBuf is a scratch buffer reused across calls to avoid allocation.
func buildReverseCSR(after *tgb.Reader, n int, depBuf *[]int32) (offsets []int32, targets []int32) {
	// Decode the forward edges once into CSR form. Calling Deps per node walks
	// the reader's offset table twice over and allocates per node; DepsCSR is a
	// single sequential pass over the column.
	fwdOff, fwdTgt, err := after.DepsCSR()
	if err != nil {
		return make([]int32, n+1), nil
	}

	// Pass 1: count in-degrees.
	inDeg := make([]int32, n)
	for _, d := range fwdTgt {
		if d >= 0 && int(d) < n {
			inDeg[d]++
		}
	}

	// Pass 2: prefix-sum → offsets (length n+1).
	offsets = make([]int32, n+1)
	var total int32
	for i := 0; i < n; i++ {
		offsets[i] = total
		total += inDeg[i]
	}
	offsets[n] = total

	// Pass 3: scatter edges.
	targets = make([]int32, total)
	pos := make([]int32, n)
	copy(pos, offsets[:n])

	for i := 0; i < n && i+1 < len(fwdOff); i++ {
		for k := fwdOff[i]; k < fwdOff[i+1]; k++ {
			d := fwdTgt[k]
			if d >= 0 && int(d) < n {
				targets[pos[d]] = int32(i)
				pos[d]++
			}
		}
	}

	return offsets, targets
}
