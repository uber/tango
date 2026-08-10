package tgb

import "github.com/uber/tango/entity"

// Graph is one complete target graph at one revision: the merged form of a
// chunked GetTargetGraphResponse stream (all target chunks concatenated, all
// metadata chunks merged). This is what Encode consumes and Decode produces.
type Graph struct {
	Targets  []entity.OptimizedTarget
	Metadata entity.Metadata
}

// SplitLabelString splits a canonical label into the (pkg, name) pair the
// encoder sorts on: everything before the last ':' and everything after it.
// Labels without a ':' get pkg == "" — the same convention the encoder uses,
// so the result round-trips through Reader.FindNode.
func SplitLabelString(label string) (pkg, name string) {
	return splitLabel(label)
}
