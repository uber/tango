package tgb

import "encoding/binary"

// appendUvarint appends a uvarint-encoded v to b and returns the result.
// It is a thin alias so call-sites don't import "encoding/binary" directly.
func appendUvarint(b []byte, v uint64) []byte {
	return binary.AppendUvarint(b, v)
}

// readUvarint reads one uvarint from b, returning the value and the number of
// bytes consumed. Returns (0, 0) on empty input or (0, -1) on overflow.
func readUvarint(b []byte) (uint64, int) {
	return binary.Uvarint(b)
}

// appendZigzag encodes v as a zigzag varint (signed → unsigned mapping) and
// appends it to b.
func appendZigzag(b []byte, v int64) []byte {
	u := uint64((v << 1) ^ (v >> 63))
	return binary.AppendUvarint(b, u)
}

// readZigzag reads one zigzag-encoded int64 from b.
func readZigzag(b []byte) (int64, int) {
	u, n := binary.Uvarint(b)
	return int64((u >> 1) ^ -(u & 1)), n
}
