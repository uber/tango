// Package tgb implements the TGB v1 (Target Graph Binary) columnar codec.
//
// File layout:
//
//	Header (64 B) | Column blobs ... | Column directory | Footer (16 B)
//
// The footer holds the column-directory offset so a reader can seek to it
// directly and fetch any single column with one ranged read.
package tgb

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

// ─── Magic / version ──────────────────────────────────────────────────────────

const (
	magic = "TGB1"
	// formatVersion history:
	//   2: BLOCK_DIGEST changed from a hash over (pkgID, nameID, ruleTypeID,
	//      degrees, flags) to a hash over label bytes alone, making the digest
	//      independent of dictionary ID assignment.
	//   3: hash stride moved from header flag bit 0 into an explicit uint16
	//      header field at offset 32 (8, 16, or 20 bytes), the NODE_INDEX
	//      column stopped being written, and FLAGS gained a third bitset
	//      marking targets whose upstream hash is the empty cycle sentinel.
	// The reader rejects any other version outright.
	formatVersion = uint16(3)
	headerSize    = 64
	footerSize    = 16

	// maxDirectoryEntries bounds the column count claimed by a blob's
	// directory. 21 column IDs are defined; 64 leaves room for extensions
	// while keeping a hostile count from driving allocation.
	maxDirectoryEntries = 64

	// maxColumnRawSize bounds the decompressed size any single column may
	// claim (1 GiB). The largest real column at 2.8M nodes is DEPS at ~22 MB
	// raw; anything near this cap is not a real graph.
	maxColumnRawSize = uint64(1) << 30

	// maxZstdExpansion bounds rawSize/compressedSize for zstd columns. The
	// most compressible real column (TAG_DEG, almost all zeros) expands
	// ~75x; 4096x is generous headroom while still stopping a kilobyte
	// blob from claiming gigabytes.
	maxZstdExpansion = uint64(4096)

	// maxTotalRawFactor bounds the *sum* of claimed rawSize across all
	// columns relative to the blob size, with maxTotalRawFloor as an
	// absolute floor so tiny legitimate blobs are never rejected. The
	// per-column expansion cap alone is not enough: a small blob full of
	// RLE frames can honestly expand 30,000x, and everything downstream
	// (dict expansion, degree indexes, Decode) allocates in proportion to
	// raw bytes. The real graph measures 1.81x raw/file; 16x is ~9x
	// headroom.
	maxTotalRawFactor = uint64(16)
	maxTotalRawFloor  = uint64(4) << 20

	// maxDictExpansionFactor bounds the total expanded string bytes a
	// front-coded dictionary may produce relative to its raw encoded size,
	// with maxDictExpansionFloor as the absolute floor. Front-coding lets
	// every ~2-byte entry claim the whole previous string as its prefix, so
	// without a budget k entries can produce O(k²) bytes. The worst real
	// dictionary (PKG_DICT, deeply nested bazel packages) expands 5.1x;
	// 32x is ~6x headroom.
	maxDictExpansionFactor = 32
	maxDictExpansionFloor  = 1 << 20
)

// Header flag bits.
const (
	// flag bit 0 carried "hashes truncated to 8 bytes" before version 3; the
	// stride is now an explicit header field. The bit must be zero.
	flagRevCSR = uint16(1 << 1) // reverse-CSR column present (reserved)
)

// ─── Column IDs ───────────────────────────────────────────────────────────────

const (
	colPkgDict       = uint64(1)
	colNameDict      = uint64(2)
	colNodePkg       = uint64(3)
	colNodeName      = uint64(4)
	colHash          = uint64(5)
	colDeg           = uint64(6)
	colDeps          = uint64(7)
	colRuleType      = uint64(8)
	colRuleTypeDict  = uint64(9)
	colTagDeg        = uint64(10)
	colTags          = uint64(11)
	colTagDict       = uint64(12)
	colAttrDeg       = uint64(13)
	colAttrName      = uint64(14)
	colAttrValue     = uint64(15)
	colAttrNameDict  = uint64(16)
	colAttrValueDict = uint64(17)
	colFlags         = uint64(18)
	colBlockDigest   = uint64(19)
	colBlockStart    = uint64(20)
	// colNodeIndex (21) is reserved. It carried sampled byte offsets into DEPS
	// (one per 1024 nodes) for random access, but no reader path ever used it
	// — ensureDepOffsets builds a full offset table and DepsCSR reads in bulk
	// — so the encoder stopped writing it. Do not reuse the ID.
	colNodeIndex = uint64(21)
	// colAllTargetsFileHashes (22) stores the AllTargetsFileHashes sidecar:
	// a length-prefixed sequence of (key, value) string pairs. Old readers
	// that don't know this ID skip it; new readers on old blobs get nil.
	colAllTargetsFileHashes = uint64(22)
)

// colCodec values stored in the directory.
const (
	codecRaw  = byte(0)
	codecZstd = byte(1)
)

// ─── Column names (for ColumnStats) ──────────────────────────────────────────

var colNames = map[uint64]string{
	colPkgDict:       "PKG_DICT",
	colNameDict:      "NAME_DICT",
	colNodePkg:       "NODE_PKG",
	colNodeName:      "NODE_NAME",
	colHash:          "HASH",
	colDeg:           "DEG",
	colDeps:          "DEPS",
	colRuleType:      "RULETYPE",
	colRuleTypeDict:  "RULETYPE_DICT",
	colTagDeg:        "TAG_DEG",
	colTags:          "TAGS",
	colTagDict:       "TAG_DICT",
	colAttrDeg:       "ATTR_DEG",
	colAttrName:      "ATTR_NAME",
	colAttrValue:     "ATTR_VALUE",
	colAttrNameDict:  "ATTR_NAME_DICT",
	colAttrValueDict: "ATTR_VALUE_DICT",
	colFlags:         "FLAGS",
	colBlockDigest:   "BLOCK_DIGEST",
	colBlockStart:    "BLOCK_START",
	colNodeIndex:             "NODE_INDEX",
	colAllTargetsFileHashes: "ALL_TARGETS_FILE_HASHES",
}

// ─── Header ───────────────────────────────────────────────────────────────────

type fileHeader struct {
	nodeCount uint64
	edgeCount uint64
	blockSize uint64
	flags     uint16
	hashBytes uint16 // stride of the HASH column: 8, 16, or 20
}

func encodeHeader(h fileHeader) [headerSize]byte {
	var b [headerSize]byte
	copy(b[0:4], magic)
	binary.LittleEndian.PutUint16(b[4:6], formatVersion)
	binary.LittleEndian.PutUint16(b[6:8], h.flags)
	binary.LittleEndian.PutUint64(b[8:16], h.nodeCount)
	binary.LittleEndian.PutUint64(b[16:24], h.edgeCount)
	binary.LittleEndian.PutUint64(b[24:32], h.blockSize)
	binary.LittleEndian.PutUint16(b[32:34], h.hashBytes)
	// bytes 34..55 reserved, zero
	crc := crc32.Checksum(b[0:56], crc32.MakeTable(crc32.Castagnoli))
	binary.LittleEndian.PutUint32(b[56:60], crc)
	// bytes 60..63 reserved, zero
	return b
}

func decodeHeader(b [headerSize]byte) (fileHeader, error) {
	if string(b[0:4]) != magic {
		return fileHeader{}, fmt.Errorf("tgb: bad magic: %q", string(b[0:4]))
	}
	ver := binary.LittleEndian.Uint16(b[4:6])
	if ver != formatVersion {
		return fileHeader{}, fmt.Errorf("tgb: unsupported version %d", ver)
	}
	// verify CRC over bytes 0..55 (spec says zero-extended to 56 bytes)
	crc := crc32.Checksum(b[0:56], crc32.MakeTable(crc32.Castagnoli))
	stored := binary.LittleEndian.Uint32(b[56:60])
	if crc != stored {
		return fileHeader{}, fmt.Errorf("tgb: header CRC mismatch: got %08x want %08x", stored, crc)
	}
	flags := binary.LittleEndian.Uint16(b[6:8])
	hashBytes := binary.LittleEndian.Uint16(b[32:34])
	switch hashBytes {
	case 8, 16, 20:
	default:
		return fileHeader{}, fmt.Errorf("tgb: invalid hash stride %d (want 8, 16, or 20)", hashBytes)
	}
	return fileHeader{
		nodeCount: binary.LittleEndian.Uint64(b[8:16]),
		edgeCount: binary.LittleEndian.Uint64(b[16:24]),
		blockSize: binary.LittleEndian.Uint64(b[24:32]),
		flags:     flags,
		hashBytes: hashBytes,
	}, nil
}

// ─── Footer ───────────────────────────────────────────────────────────────────

type fileFooter struct {
	dirOffset uint64
	dirCRC    uint32
}

func encodeFooter(f fileFooter) [footerSize]byte {
	var b [footerSize]byte
	binary.LittleEndian.PutUint64(b[0:8], f.dirOffset)
	binary.LittleEndian.PutUint32(b[8:12], f.dirCRC)
	copy(b[12:16], magic)
	return b
}

func decodeFooter(b [footerSize]byte) (fileFooter, error) {
	if string(b[12:16]) != magic {
		return fileFooter{}, fmt.Errorf("tgb: bad footer magic: %q", string(b[12:16]))
	}
	return fileFooter{
		dirOffset: binary.LittleEndian.Uint64(b[0:8]),
		dirCRC:    binary.LittleEndian.Uint32(b[8:12]),
	}, nil
}

// ─── Column directory entry ────────────────────────────────────────────────────

type colEntry struct {
	id             uint64
	codec          byte
	offset         uint64
	compressedSize uint64
	rawSize        uint64
}

// encodeDirectory serialises column entries as uvarint-framed records.
func encodeDirectory(entries []colEntry) []byte {
	var buf []byte
	buf = binary.AppendUvarint(buf, uint64(len(entries)))
	for _, e := range entries {
		buf = binary.AppendUvarint(buf, e.id)
		buf = append(buf, e.codec)
		buf = binary.AppendUvarint(buf, e.offset)
		buf = binary.AppendUvarint(buf, e.compressedSize)
		buf = binary.AppendUvarint(buf, e.rawSize)
	}
	return buf
}

// decodeDirectory parses the column directory from b.
func decodeDirectory(b []byte) ([]colEntry, error) {
	n, sz := binary.Uvarint(b)
	if sz <= 0 {
		return nil, fmt.Errorf("tgb: truncated directory count")
	}
	// The count is attacker-controlled input; allocating make(..., 0, n)
	// directly would let a 10-byte blob demand petabytes. There are 21
	// defined column IDs; anything claiming more is not a TGB file.
	if n > maxDirectoryEntries {
		return nil, fmt.Errorf("tgb: directory claims %d columns (max %d)", n, maxDirectoryEntries)
	}
	b = b[sz:]
	entries := make([]colEntry, 0, n)
	for i := uint64(0); i < n; i++ {
		id, sz := binary.Uvarint(b)
		if sz <= 0 {
			return nil, fmt.Errorf("tgb: truncated column id")
		}
		b = b[sz:]
		if len(b) == 0 {
			return nil, fmt.Errorf("tgb: truncated codec byte")
		}
		codec := b[0]
		b = b[1:]
		off, sz := binary.Uvarint(b)
		if sz <= 0 {
			return nil, fmt.Errorf("tgb: truncated offset")
		}
		b = b[sz:]
		comp, sz := binary.Uvarint(b)
		if sz <= 0 {
			return nil, fmt.Errorf("tgb: truncated compressedSize")
		}
		b = b[sz:]
		raw, sz := binary.Uvarint(b)
		if sz <= 0 {
			return nil, fmt.Errorf("tgb: truncated rawSize")
		}
		b = b[sz:]
		entries = append(entries, colEntry{
			id:             id,
			codec:          codec,
			offset:         off,
			compressedSize: comp,
			rawSize:        raw,
		})
	}
	return entries, nil
}
