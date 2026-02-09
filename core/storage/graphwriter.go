package storage

import (
	"bytes"
	"context"
	"fmt"

	gogio "github.com/gogo/protobuf/io"
	pb "github.com/uber/tango/tangopb"
)

// WriteGraphStream writes a list of GetTargetGraphResponse messages to the storage.
func WriteGraphStream(ctx context.Context, st Storage, key string, graphs []*pb.GetTargetGraphResponse) error {
	buf := &bytes.Buffer{}
	// For testing purposes, write the entire target graph into a single message and Metadata as a separate message.
	// TODO: Update writer to write a list of []OptimizedTargets and Metadata at the end for the storage to allow reading message into a buffered array.

	w := gogio.NewDelimitedWriter(buf) // varint-length-delimited
	for _, g := range graphs {
		if err := w.WriteMsg(g); err != nil {
			return fmt.Errorf("write delimited: %w", err)
		}
	}
	return st.Put(ctx, UploadRequest{Key: key, Reader: bytes.NewReader(buf.Bytes())})
}
