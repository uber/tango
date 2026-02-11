package storage

import (
	"bufio"
	"context"
	"io"

	pb "github.com/uber/tango/tangopb"
	"google.golang.org/protobuf/encoding/protodelim"
)

type GraphReader interface {
	// Read reads the next GetTargetGraphResponse message from the storage.
	Read() (*pb.GetTargetGraphResponse, error)
	// Close releases any underlying resources if supported by the implementation.
	// Implementations that don't hold resources may return nil.
	Close() error
}

// graphReader is a io.Reader that, when Read is invoked,
// streams the delimited GetTargetGraphResponse messages from Storage to the provided YARPC server stream.
// After streaming completes, subsequent Read calls return io.EOF.
type graphReaderCloser struct {
	reader        *bufio.Reader
	closer        io.Closer
	unmarshalOpts protodelim.UnmarshalOptions
}

// Read reads the next message from the storage.
func (g *graphReaderCloser) Read() (*pb.GetTargetGraphResponse, error) {
	m := new(pb.GetTargetGraphResponse)
	err := g.unmarshalOpts.UnmarshalFrom(g.reader, m)
	if err != nil {
		return nil, err
	}
	if m.GetItem() == nil {
		return nil, io.EOF
	}
	return m, nil
}

func (g *graphReaderCloser) Close() error {
	if g.closer != nil {
		return g.closer.Close()
	}
	return nil
}

// NewGraphReader returns a GraphReader that, when read, will fetch the stored graph at key
func NewGraphReader(ctx context.Context, st Storage, key string) (GraphReader, error) {
	resp, err := st.Get(ctx, DownloadRequest{Key: key})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.ReadCloser == nil {
		return nil, nil
	}
	return &graphReaderCloser{
		reader:        bufio.NewReader(resp.ReadCloser),
		closer:        resp.ReadCloser,
		unmarshalOpts: protodelim.UnmarshalOptions{MaxSize: 32 << 20}, // 32MB/message limit
	}, nil
}
