package wire

// VarintSize returns the number of bytes needed to encode x as a protobuf varint.
func VarintSize(x uint64) int {
	n := 1
	for x >= 0x80 {
		x >>= 7
		n++
	}
	return n
}
