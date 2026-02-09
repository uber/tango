package storage

import (
	"bytes"
	"context"
	"fmt"

	gogio "github.com/gogo/protobuf/io"
	pb "github.com/uber/tango/tangopb"
)

// WriteGraphStream writes a list of GetTargetGraphResponse messages to the storage.
// The messages are written as length-delimited protobuf, allowing streaming reads.
// Typically this includes multiple OptimizedTargets chunks followed by Metadata.
func WriteGraphStream(ctx context.Context, st Storage, key string, graphs []*pb.GetTargetGraphResponse) error {
	buf := &bytes.Buffer{}
	w := gogio.NewDelimitedWriter(buf) // varint-length-delimited
	for _, g := range graphs {
		if err := w.WriteMsg(g); err != nil {
			return fmt.Errorf("write delimited: %w", err)
		}
	}
	return st.Put(ctx, UploadRequest{Key: key, Reader: bytes.NewReader(buf.Bytes())})
}
