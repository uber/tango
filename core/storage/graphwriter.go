package storage

import (
	"bytes"
	"context"
	"fmt"

	pb "github.com/uber/tango/tangopb"
	"google.golang.org/protobuf/encoding/protodelim"
)

// WriteGraphStream writes a list of GetTargetGraphResponse messages to the storage.
// The messages are written as length-delimited protobuf, allowing streaming reads.
// Typically this includes multiple OptimizedTargets chunks followed by Metadata.
func WriteGraphStream(ctx context.Context, st Storage, key string, responses []*pb.GetTargetGraphResponse) error {
	buf := &bytes.Buffer{}
	for _, r := range responses {
		if _, err := protodelim.MarshalTo(buf, r); err != nil {
			return fmt.Errorf("write delimited: %w", err)
		}
	}
	return st.Put(ctx, UploadRequest{Key: key, Reader: bytes.NewReader(buf.Bytes())})
}
