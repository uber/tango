# Tango Graph Binary: columnar encoding and comparison algorithm for bazel target graph

---

## 1. Problem

Tango computes which Bazel targets are affected by a change. The pipeline is:

A BuildKite CI machine runs `bazel query` over the monorepo at the *before* and *after* revisions, and builds a target graph for each.

1. Each graph is serialized and uploaded to TerraBlob.
2. A server downloads both graphs and diffs them to produce the affected set.

Every stage is currently paying for a representation that was chosen for
convenience rather than for the shape of the data or the shape of the query.

### 1.1 What it costs today

Measured on a large go monorepo with ~3M nodes and ~14M edges, from an actual `bazel query --output=streamed_proto` over the whole monorepo:


| Stage                  | Cost today                                           |
| ---------------------- | ---------------------------------------------------- |
| Serialized size (gob)  | **548.8 MB** per revision                            |
| Serialized size (JSON) | **937.6 MB** per revision                            |
| Decode, server side    | **4.72 s** per blob, 4.6 GB allocated                |
| Compare                | **15.7 s** (leaf change) to **29.6 s** (wide change) |


### 1.2 Why it is large

The current encoding scheme for tango's graph binary suffers from 5 major issues:


| Cause                         | Cost                                                                                                                                                                              |
| ----------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| No compression anywhere       | The gob stream is written raw through `io.Pipe` into `Storage.Put`. Bazel label text is extremely repetitive and compresses 5–10×.                                                |
| `Hash` stored as a hex string | 40 ASCII bytes per node where 8 raw bytes suffice.                                                                                                                                |
| Dependency edges as `[]int32` | Full 4-byte IDs, no delta coding, despite deps being strongly clustered near their parent in label order.                                                                         |
| Row-oriented records          | Every node carries its own field tags and lengths; gob emits per-value type framing. Volatile and stable data are interleaved, so nothing can be cached or reused across commits. |
| Reflection-driven codec       | `encoding/gob` walks struct types at runtime and allocates per value.                                                                                                             |



### 1.3 Why it is slow

The dominant cost for comparison is actually not the diffing algorithm itself. It is **materialising two** `map[string]*Target` **graphs of ~2.8M entries each** in order to have something to diff. In the measured leaf case, `resolve` is 7.84 s of refdiff's 15.72 s, and the two gob decodes that precede it are another 9.44 s.

That work is proportional to the size of the graph, not to the size of the change. A typical PR changes only a handful of targets out of 2.8M, and the server pays full price for the 99.999% that did not change.

**This is the central observation the whole design follows from.** The format should make "these two nodes are identical" — overwhelmingly the common answer — cost close to nothing.

---



## 2. Goals and non-goals

**Goals**

1. Small on the wire.
2. Fast decode.
3. Comparable without decoding. The common case should happen by bulk `memcmp`, never by a map lookup.
4. Byte-identical output. The new comparison must produce exactly the same `DiffResult` as today's algorithm. i.e. this should be a drop-in replacement.

**Non-Goals**

- Random access to a single target by label. The proposed format here can technically support it via dictionary restart points, but nothing in the pipeline needs it yet. This can be implemented as needed in the future.
- Streaming decode. The blob is fetched whole. The proposed format will have to substantially change for this to be supported, since the columnar layout does prevent this from occurring in streaming fashion.

---

## 3. Design principles

1. **Columnar (struct-of-arrays).** Each field becomes its own contiguous stream. This isolates the one volatile column (`HASH`) from the many stable runs, lets each column pick its own codec, and lets the reader touch only the columns a given query needs.
2. **Deterministic total order.** Nodes are sorted by the `(package, name)`tuple. Sorted order is what makes prefix compression, delta coding, and merge-join diffing all possible simultaneously.
3. **Intern everything, front-code the dictionaries.** Sorted string dictionaries share long prefixes; store only the delta.
4. **Delta + varint every integer column.** In label order, a node's dependencies mostly live a few indices away.
5. I**ndependently framed, addressable columns.** A directory holds `(offset, rawSize, compSize, codec)` per column, so columns can decompress in parallel and unused columns are never touched.
6. **Deliberately uncompressed hash column.** Fixed stride, contiguous, raw - so comparing a run of N nodes can happen through `bytes.Equal`. We can, in theory, compress even further by compressing the hash, but that makes the comparison impossible w/o decompression - here we trade off the total binary size for comparison speed later in the pipeline.

---



## 4. TGB Container format

```
+--------------------------------------------------+
| Header (64 B, fixed)                             |
+--------------------------------------------------+
| Column blob 0 .. N                               |
+--------------------------------------------------+
| Column directory                                 |
+--------------------------------------------------+
| Footer (16 B, fixed)                             |
+--------------------------------------------------+
```



### Header — 64 bytes, little-endian


| Offset | Size | Field                                                                          |
| ------ | ---- | ------------------------------------------------------------------------------ |
| 0      | 4    | magic `"TGB1"`                                                                 |
| 4      | 2    | formatVersion                                                                  |
| 6      | 2    | flags (bit 0: hashes truncated to 8 B; bit 1: reverse-CSR present, *reserved*) |
| 8      | 8    | nodeCount                                                                      |
| 16     | 8    | edgeCount                                                                      |
| 24     | 8    | blockSize                                                                      |
| 32     | 24   | reserved, zero                                                                 |
| 56     | 4    | checksum (CRC32C) over bytes 0..55                                             |
| 60     | 4    | reserved, zero                                                                 |




### Footer — 16 bytes


| Offset | Size | Field                 |
| ------ | ---- | --------------------- |
| 0      | 8    | columnDirectoryOffset |
| 8      | 4    | columnDirectoryCRC32C |
| 12     | 4    | magic `"TGB1"`        |


A reader seeks to `fileSize-16`, reads the footer, seeks to the directory, and
from there can fetch any single column with one ranged read. `NewReader` validates both CRCs and **decompresses nothing**.

### Column directory

`uvarint(columnCount)`, then per column:
`uvarint id`, `byte codec` (0 = raw, 1 = zstd), `uvarint offset`,
`uvarint compressedSize`, `uvarint rawSize`.

Columns may appear in any order; an absent column decodes as empty.

---



## 5. Columns

Nodes are numbered `0 .. nodeCount-1` in sorted `(pkg, name)` order. "Node
index" always means this ordinal, and it is the currency the entire comparison
runs in.


| ID    | Name                                  | Codec   | Contents                                        |
| ----- | ------------------------------------- | ------- | ----------------------------------------------- |
| 1     | `PKG_DICT`                            | zstd    | Front-coded sorted package paths                |
| 2     | `NAME_DICT`                           | zstd    | Front-coded sorted target names                 |
| 3     | `NODE_PKG`                            | zstd    | Per node: uvarint delta of package ID           |
| 4     | `NODE_NAME`                           | zstd    | Per node: name ID, delta-coded within a package |
| 5     | `HASH`                                | **raw** | Per node: 8 bytes, truncated Merkle hash        |
| 6     | `DEG`                                 | zstd    | Per node: uvarint out-degree                    |
| 7     | `DEPS`                                | zstd    | Delta-coded dependency node indices             |
| 8     | `RULETYPE`                            | zstd    | Per node: uvarint rule-type dict ID             |
| 9     | `RULETYPE_DICT`                       | zstd    | Front-coded sorted rule-type strings            |
| 10–12 | `TAG_DEG`, `TAGS`, `TAG_DICT`         | zstd    | Same shape as deps                              |
| 13–15 | `ATTR_DEG`, `ATTR_NAME`, `ATTR_VALUE` | zstd    | Per node: attribute name/value dict IDs         |
| 16–17 | `ATTR_NAME_DICT`, `ATTR_VALUE_DICT`   | zstd    | Front-coded sorted strings                      |
| 18    | `FLAGS`                               | zstd    | Two bitsets, `root` then `external`             |
| 19    | `BLOCK_DIGEST`                        | raw     | Per block: 8-byte structural digest             |
| 20    | `BLOCK_START`                         | zstd    | Per block: uvarint delta of first node index    |
| 21    | `NODE_INDEX`                          | zstd    | Sampled byte offsets into `DEPS` (see §8.6)     |




### 5.1 Front-coded dictionaries

Strings sorted ascending; per entry:

```
uvarint sharedPrefixLen   // shared with the previous entry
uvarint suffixLen
byte[]  suffix
```

Bazel labels are deep paths; deeply nested packages in a large-monorepo typically share 40–60 characters (ex. `//src/code.uber.internal/...)`

Every 1024th entry is a restart point emitted with `sharedPrefixLen = 0`, and the blob carries a table of restart offsets so a dictionary *could* be binary-searched without full expansion. This can be leveraged in the future for allowing the spec to support random-lookups in the map. (More details on this later).

### 5.2 `NODE_PKG` — sorted

Because nodes are sorted by `(pkg, name)`, the package ID sequence is **non-decreasing**. Storing `pkgID[i] - pkgID[i-1]` yields a sequence that is mostly zeros with an occasional one, which zstd reduces to almost nothing: **.27 MB for 2.8M nodes**, 0.10 bytes per node.

This monotonicity is load-bearing. It is why the sort key is the `(pkg, name)`tuple rather than the joined label string — see §6.1, where getting this subtlety wrong caused a real bug.

### 5.3 `NODE_NAME`

Within a package names are also ascending, so store `nameID[i] - nameID[i-1]` when node `i` shares a package with `i-1`, and the absolute `nameID[i]`otherwise.

### 5.4 `DEPS` — delta-coded edges

Per node, dependencies are resolved to node indices, sorted ascending, then:

```
first dep : zigzag_varint(dep[0] - nodeIndex)
rest      : uvarint(dep[k] - dep[k-1])
```

Encoding the first dep *relative to the node itself* is what exploits locality. For example: a `go_library` depends on its own source files and on siblings, all of which is sort of adjacent to it. 

Measured: **9.96 MB compressed for 13.8M edges = 0.72 bytes per edge**, against 4 bytes today.

Dependency arrays are concatenated with no per-node framing; `DEG` supplies the boundaries.

### 5.5 `HASH` — deliberately uncompressed

Truncated to the leading 8 bytes of the SHA-1 Merkle hash. This column is not
compressed, and that is the point:

- Hashes are uniformly random, so compression buys under 1% anyway.
- Leaving it raw makes it a flat `[]byte` with fixed stride 8. Comparing 4096
consecutive nodes across two revisions is `bytes.Equal` over a 32 KB slice —
memory-bandwidth-bound.

Cost: 21.52 MB, **41% of the entire 52.2 MB blob**. A full linear scan of both
revisions costs about 5 ms.

**Collision risk.** Across 5.6M values (both revisions) the birthday probability of any 8-byte collision is roughly 8×10^-7. A collision produces a false *negative* — a changed target reported as unchanged — which in this pipeline means a test that should have run does not. That is a real correctness risk, hence the hash stride width must be configurable. The header has a flag bit for it.

### 5.6 `BLOCK_DIGEST` and content-defined boundaries

Blocks partition the node sequence. A boundary is cut after node `i` when

```
xxhash64(label_i) & (blockSize-1) == 0
```

with `blockSize` a power of two (4096 default), plus a forced cut if a block would exceed `blockSize*8` nodes.

Boundaries are **content-defined, not positional**. 

This is essential: inserting or deleting a node perturbs only the block containing it, and boundaries downstream re-align naturally. Positional blocks would shift on every insertion and defeat the mechanism entirely.

Each block's digest is `xxhash64` over the **label bytes** of its nodes, NUL-separated. Labels cannot contain NUL, so no two distinct label sequences produce the same byte stream.

Note what the digest deliberately excludes: deps, tags, attributes, rule types, and degrees. Its contract is exactly **"these nodes carry these labels, in this order"** — which is all phase 0 needs, because matching a digest *aligns* two runs, it never exempts them from phase 1. Any change to a node's deps or attributes moves its Merkle hash, and phase 1 catches it.

Hashing label bytes rather than the `(pkgID, nameID)` pair is what makes the digest **dictionary-independent**, and that property is load-bearing. Dictionaries are globally sorted, so inserting a single target whose name sorts early shifts every subsequent `nameID` by one.

---



## 6. Comparison Algorithm

The comparison must not materialize `map[string]*Target` from the binary format and must support diffing against an encoded TGB scheme. This section describes how that works.

The comparison algorithm runs in four phases over node indices, and resolves a label string exactly once per *changed* node, at the very end.

### 6.0 Phase 0 — align the two node sequences

Walk the two `BLOCK_DIGEST` arrays. Where a digest appears in both, the labels in that block correspond one-to-one; the block is aligned wholesale with no labels decoded and no dictionary touched. Only non-matching blocks have their labels decoded and merge-joined locally.

Output: a run list of maximal `(beforeStart, afterStart, length)` triples where the two graphs hold the same labels at the same relative offsets, plus the inserted and deleted node indices between runs.

For a PR touching a few packages, this decodes a handful of blocks only and never reads the rest of the graph.

### 6.1 Note on ordering subtlety in the merge-join

The merge-join walks both unmatched lists in the encoder's node order. 

That order is ascending by the `(pkg, name)` **tuple**, and that is *not* lexicographic order on the joined `pkg:name` label, because `/` (0x2F) sorts below `:` (0x3A):

```
tuple order:  ("//a", "t") < ("//a/b", "t")
label order:  "//a/b:t"    < "//a:t"          ← opposite
```

Nested packages are ubiquitous in Bazel — `//src/foo` and `//src/foo/bar` routinely coexist — so this is the common case, not an edge case.

Comparing the joined label strings here *doesn't work* here - that walks two lists in an order neither is sorted in, and mispairs them. Instead, we must compare the `(pkg, name)` tuple — the same key the encoder sorted on — via a
`Reader.SplitLabel` accessor.

### 6.2 Phase 1 — bulk hash comparison

For each aligned run, one `bytes.Equal` over `HASH[beforeStart*8 : (beforeStart+len)*8]` against the corresponding after slice. Equal means every node in the run is unchanged, established in one call.

Where a run differs, bisect: split in half, compare each half, recurse to individual 8-byte entries. With `k` changed nodes in a run of `n`, that is `O(k log(n/k))` comparisons rather than `n`.

This is the structural inversion versus today's code, which does one map lookup and one string comparison per node *in the graph*. Here unchanged nodes cost approximately nothing and the work scales with the size of the change.

Note that phase 0 skipping a block does **not** skip it in phase 1 — matched blocks go into aligned runs and are hash-compared like everything else. This is what makes the weak block-digest contract sound: the digest only has to certify label correspondence, and the hashes are checked independently.

### 6.3 Phase 2 — seed classification

Only changed nodes need their deps, tags, and attributes. The classification rules are unchanged from `internal/targetdiff/compare.go`: a changed target is a seed if it is new, deleted, a source file, its dependency set changed shape, it has a changed source-file dependency, or its attributes changed.

- The changed set is `changedByAfter []*changedNode`, indexed by after-node index - an array, not a map.
- `beforeToAfter []int32` comes free from phase 0's aligned runs. Projecting a before-dependency into after-index space makes `-1` mean "this label does not exist in the after graph at all", which is exactly the condition the label version detected as a set change.
- Rule types are compared by dictionary ID (`RuleTypeDictID("source file")`), not by string.

Comparing indices is equivalent to comparing labels because label→index is a bijection within each graph and the projection maps equal labels to equal indices.

Result: labels decoded fell **26,516,422 → 813,096** — one per changed target, which is the floor — and phase 2 went **17.63 s → 2.83 s**.

### 6.4 Phase 3 — distance BFS on a reverse CSR

Phase 2 classified some changed nodes as *seeds* (distance 0). Phase 3 must assign every other changed node its distance: the length of the shortest dependency chain connecting it to any seed. Two things make this awkward on the data we have:

1. **The edges point the wrong way.** The `DEPS` column stores *forward*
  edges: node → the nodes it depends on. But distance propagates in the  other direction — when a target changes, it is the targets that *depend  on it* (directly or transitively) that are affected. So phase 3 needs,  for each node, "who depends on me": the transpose of the stored graph.
2. **It is a whole-graph traversal.** Every other phase touches work
  proportional to the *changed* set; a breadth-first search can reach a  large fraction of the graph (a changed core library reaches almost all  of it), so the per-node and per-edge constants dominate.

Today's implementation pays those constants in the worst currency available: the reverse adjacency is a `map[string][]string` — millions of string hashes to look up a node, a heap-allocated slice per node grown by `append`, every neighbour list somewhere else in memory. The BFS then chases pointers through all of it. The replacement below does the same traversal with two integer arrays and a bitset.

**The representation: CSR from first principles.** After phase 0, every node in the after graph is identified by a dense integer index `0..n-1` (its position in label-sorted order), so "adjacency" no longer needs a map — an array indexed by node works. The naive array-of-slices (`[][]int32`) still costs one allocation and one pointer indirection per node. CSR — *compressed sparse row*, borrowed from sparse matrix storage — flattens all of it into exactly two arrays:

- `targets []int32` — every neighbour list, concatenated in node order. Length = number of edges.
- `offsets []int32` — where each node's list starts in `targets`. Length = n+1, with `offsets[n]` = total edge count, so node `i`'s neighbours are always the half-open slice `targets[offsets[i]:offsets[i+1]]` — no special case for the last node, and a node with no neighbours simply has `offsets[i] == offsets[i+1]`.

A concrete example. Take four nodes where 1, 2, and 3 each depend on 0, and 3 also depends on 2 (forward edges: 1→0, 2→0, 3→0, 3→2). The *reverse* adjacency — who depends on me — is:

```
node 0: [1, 2, 3]      offsets: [0, 3, 3, 4, 4]
node 1: []             targets: [1, 2, 3, 3]
node 2: [3]                      └─node 0─┘└2┘
node 3: []
```

Neighbour lookup is two array reads and a slice — no hashing, no pointer chase. The whole structure is two allocations regardless of graph size, laid out contiguously so a BFS that scans neighbour lists walks memory almost sequentially. For 13.8M edges the arrays are ~55 MB and ~11 MB; the map version is several times that before counting allocator and GC overhead.

**Building the transpose in three linear passes.** The classic counting-sort construction; nothing is ever sorted or searched:

1. *Count in-degrees.* Walk every forward edge `u → v` once and increment
  `inDeg[v]`. For the example: `inDeg = [3, 0, 1, 0]`.
2. *Prefix-sum.* `offsets[i]` = sum of `inDeg[0..i-1]`. Each node's count
  becomes the start of its region: `offsets = [0, 3, 3, 4, 4]`.
3. *Scatter.* Walk the forward edges again; for each `u → v`, write `u` at
  `targets[pos[v]]` and bump `pos[v]`, where `pos` starts as a copy of  `offsets`. Each write lands in the region reserved for `v` in pass 2.

Every pass is a straight sequential scan; the output is deterministic (within one node's region, dependents appear in forward-scan order, which is node-index order). Edges whose endpoint falls outside `[0, n)` are skipped at both the counting and scatter steps — a hostile blob can claim any int32 as a dependency, and phase 3 must not let that index out of bounds.

---



## 7. Measured results

Real `go-code` graph: 2,821,235 nodes, 13,783,851 edges, 1,794,002 source-file nodes (63.6%), 375 rule types, 262.6 MB of raw label text.

### 7.1 Size


| codec       | size        | vs today  |
| ----------- | ----------- | --------- |
| gob (today) | 548.8 MB    | 1.00×     |
| gob + zstd  | 180.7 MB    | 0.33×     |
| json        | 937.6 MB    | 1.71×     |
| **TGB**     | **52.2 MB** | **0.10×** |


**10.5× smaller than today.** Per-column, the top consumers:


| column                    | raw MB     | comp MB   | B/node   |
| ------------------------- | ---------- | --------- | -------- |
| HASH                      | 21.52      | 21.52     | 8.00     |
| DEPS                      | 22.46      | 9.96      | 3.70     |
| ATTR_VALUE_DICT           | 18.51      | 5.95      | 2.21     |
| NAME_DICT                 | 16.63      | 4.97      | 1.85     |
| ATTR_VALUE                | 10.27      | 3.07      | 1.14     |
| NODE_NAME                 | 5.62       | 2.54      | 0.94     |
| PKG_DICT                  | 5.09       | 1.77      | 0.66     |
| everything else (14 cols) | 20.57      | 2.47      | 0.90     |
| **TOTAL**                 | **120.67** | **52.25** | **18.5** |


### 7.2 Encode / decode

| codec      | encode | decode          | decode alloc |
| ---------- | ------ | --------------- | ------------ |
| gob        | 5.86 s | 4.72 s          | 4596 MB      |
| gob + zstd | 8.92 s | 6.70 s          | 4613 MB      |
| TGB        | 6.9s   | **2.83–3.14 s** | **2134 MB**  |

TGB has the fastest decode with less than half the allocation, and comparable albeit slightly regressed encode time to gob. It still beats gob + zstd in encoding speed.

### 7.3 Comparison


| change shape         | changed nodes   | refdiff (today) | fastdiff          | compare-only      | end-to-end    |
| -------------------- | --------------- | --------------- | ----------------- | ----------------- | ------------- |
| leaf (`HEAD~1→HEAD`) | 30 (0.001%)     | 15.72 / 13.51 s | **1.11 / 1.18 s** | **14.2× / 11.4×** | ~23× / ~19×   |
| wide blast radius    | 813,096 (28.8%) | 29.60 / 31.35 s | **3.74 / 4.11 s** | **7.9× / 7.6×**   | 10.4× / 10.1× |


Two independent runs are shown because run-to-run variance on this box is
±10% and it moves *both* implementations together — the second run had refdiff
at 13.51 s on the leaf case against 15.72 s in the first, which alone swings the
headline ratio from 14.2× to 11.4×. Any single number here should be read as
"roughly an order of magnitude", not as three significant figures.

Phase breakdown (first run):

```
leaf   refdiff  total=15.72s  resolve=7.84 pass1=2.61 seed=0.00 pass2=1.37 revIdx=3.90 bfs=0.00
leaf   fastdiff total= 1.11s  align=0.006 hashDiff=0.014 seed=0.70 revCSR=0.21 bfs=0.002
wide   refdiff  total=29.60s  resolve=8.29 pass1=2.71 seed=9.25 pass2=1.41 revIdx=4.33 bfs=2.74
wide   fastdiff total= 3.74s  align=0.006 hashDiff=0.075 seed=2.83 revCSR=0.22 bfs=0.054
```

End-to-end adds the two 4.72 s gob decodes refdiff must pay first; fastdiff pays
essentially nothing at open because columns decode lazily.

**Correctness gate: PASS on both shapes, on both runs** — byte-identical
`DiffResult` (same targets, change types, seeds, and distances) against refdiff
fed graphs truncated to the same 8-byte hash width. Labels decoded were 30 and
813,096 respectively: exactly one per changed target in both cases.

### 7.4 Structural change

Both cases above hold the label set fixed and perturb only hashes, so every
block matches by digest and phase 0's merge-join never runs. `cmd/mkstruct`
builds the missing case: it deletes zero-in-degree targets spread across the
graph and inserts new ones, half into a brand-new package sorting before every
existing package and half under a name sorting before every existing name — the
maximally adversarial dictionary shift.


| changed targets        | refdiff | fastdiff   | speedup   | blocks decoded | labels decoded | align  |
| ---------------------- | ------- | ---------- | --------- | -------------- | -------------- | ------ |
| 40 (20 add, 20 del)    | 16.15 s | **1.17 s** | **13.8×** | 58 of ~1454    | 477,640        | 0.78 s |
| 400 (200 add, 200 del) | 15.00 s | **2.25 s** | **6.7×**  | 460 of ~1454   | 3,122,000      | 1.94 s |


Both PASS the correctness gate. (Block counts are summed over both sides, hence
~1454 rather than 727.)

Two things to read off this. The digest fix works: damage stays local, roughly
1.15 disturbed blocks per changed target, rather than the whole graph. But
`align` is now 67–86% of the total, because each disturbed block costs a full
block's worth of label decodes on both sides — see §8.3.

These are synthetic and deliberately scattered. A real PR touches a few BUILD
files and its changes cluster into far fewer blocks, so these should be read as
an adversarial bound rather than a typical case.

---
