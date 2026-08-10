package tgb

import (
	"encoding/binary"
	"fmt"
)

// restartInterval is the number of entries between restart points in a
// front-coded dictionary. Every restartInterval-th entry is stored with
// sharedPrefixLen=0 so the dictionary can be binary-searched.
const restartInterval = 1024

// buildFrontCodedDict encodes a sorted slice of strings as a front-coded
// dictionary with restart points every restartInterval entries.
//
// Wire format (per entry):
//
//	uvarint sharedPrefixLen   // 0 at restart points
//	uvarint suffixLen
//	byte[]  suffix
//
// Returns the encoded bytes and the restart-offset table (one uint64 per
// restart entry recording the byte offset within the returned blob where
// that restart entry begins). The offset table is prepended to the blob so
// the binary can locate restart points without a separate column.
//
// Layout of the returned []byte:
//
//	uvarint restartCount
//	uint64 × restartCount   (little-endian absolute byte offsets from the
//	                          start of the string-data section below)
//	<string data>
func buildFrontCodedDict(strs []string) []byte {
	if len(strs) == 0 {
		var buf []byte
		buf = binary.AppendUvarint(buf, 0) // restartCount = 0
		return buf
	}

	// First pass: collect restart byte offsets and build the string data.
	var strData []byte
	var restartOffsets []uint64
	prev := ""
	for i, s := range strs {
		entryOffset := uint64(len(strData))
		shared := 0
		if i%restartInterval != 0 {
			shared = commonPrefixLen(prev, s)
		} else {
			// Restart point: emit sharedPrefixLen=0 and record the offset.
			restartOffsets = append(restartOffsets, entryOffset)
		}
		suffix := s[shared:]
		strData = binary.AppendUvarint(strData, uint64(shared))
		strData = binary.AppendUvarint(strData, uint64(len(suffix)))
		strData = append(strData, suffix...)
		prev = s
	}

	// Build the final blob: restartCount | offsets | string data.
	var buf []byte
	buf = binary.AppendUvarint(buf, uint64(len(restartOffsets)))
	for _, off := range restartOffsets {
		buf = binary.LittleEndian.AppendUint64(buf, off)
	}
	buf = append(buf, strData...)
	return buf
}

// expandDict decodes a front-coded dictionary produced by buildFrontCodedDict
// and returns all strings.
func expandDict(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	// Read restartCount. Bound it by the bytes actually present before
	// multiplying: rc is attacker-controlled and rc*8 can wrap uint64.
	rc, n := binary.Uvarint(data)
	if n <= 0 {
		return nil, errorf("dict: truncated restartCount")
	}
	data = data[n:]
	if rc > uint64(len(data))/8 {
		return nil, errorf("dict: restartCount %d exceeds data", rc)
	}

	// Skip the restart-offset table.
	data = data[rc*8:]

	// Decode string entries. Front-coding lets each ~2-byte entry claim the
	// whole previous string as its prefix, so a hostile dictionary can
	// produce O(k²) output bytes from k entries; budget the total expansion
	// against the encoded size (the worst real dictionary expands 5.1x).
	budget := len(data) * maxDictExpansionFactor
	if budget < maxDictExpansionFloor {
		budget = maxDictExpansionFloor
	}
	encodedLen := len(data)
	var out []string
	total := 0
	prev := ""
	for len(data) > 0 {
		shared, n := binary.Uvarint(data)
		if n <= 0 {
			return nil, errorf("dict: truncated sharedPrefixLen")
		}
		data = data[n:]
		suffLen, n := binary.Uvarint(data)
		if n <= 0 {
			return nil, errorf("dict: truncated suffixLen")
		}
		data = data[n:]
		if uint64(len(data)) < suffLen {
			return nil, errorf("dict: suffix truncated")
		}
		suf := string(data[:suffLen])
		data = data[suffLen:]
		if int(shared) > len(prev) {
			return nil, errorf("dict: shared prefix %d > prev len %d", shared, len(prev))
		}
		s := prev[:shared] + suf
		total += len(s)
		if total > budget {
			return nil, errorf("dict: expands to %d+ bytes from %d encoded (max %d)", total, encodedLen, budget)
		}
		out = append(out, s)
		prev = s
	}
	return out, nil
}

// restartTable returns the restart-offset table from an encoded dict blob
// (the absolute byte offsets into the string-data section, adjusted so callers
// can index directly into the string-data slice).
func restartTable(data []byte) (restartOffsets []uint64, strData []byte, err error) {
	rc, n := binary.Uvarint(data)
	if n <= 0 {
		return nil, nil, errorf("dict: truncated restartCount")
	}
	data = data[n:]
	// Bound rc by the bytes actually present before multiplying or
	// allocating: rc is attacker-controlled, rc*8 can wrap uint64, and
	// make([]uint64, rc) would allocate off the raw claim.
	if rc > uint64(len(data))/8 {
		return nil, nil, errorf("dict: restartCount %d exceeds data", rc)
	}
	tableBytes := rc * 8
	offsets := make([]uint64, rc)
	for i := range offsets {
		offsets[i] = binary.LittleEndian.Uint64(data[i*8:])
	}
	return offsets, data[tableBytes:], nil
}

// lookupDictEntry returns the string at index idx inside the already-expanded
// []string slice. For the lazy Reader we expand on first access and cache.
func lookupDictEntry(expanded []string, idx int) (string, error) {
	if idx < 0 || idx >= len(expanded) {
		return "", errorf("dict: index %d out of range [0,%d)", idx, len(expanded))
	}
	return expanded[idx], nil
}

// commonPrefixLen returns the number of bytes a and b share at the start.
func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func errorf(f string, a ...any) error {
	return fmt.Errorf("tgb: "+f, a...)
}
