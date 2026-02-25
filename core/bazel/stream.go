package bazel

import (
	"bufio"
	"context"
	"fmt"
	"io"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"google.golang.org/protobuf/encoding/protodelim"
)

// streamOutput copies data from src to dst, checking context periodically
func streamOutput(ctx context.Context, src io.Reader, dst io.Writer) error {
	buf := make([]byte, 32*1024)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				// Write failed but MUST keep reading to drain pipe
				for {
					if _, err := src.Read(buf); err != nil {
						break
					}
				}
				return writeErr
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// streamAndParseTargets reads delimited Target protos from src
func streamAndParseTargets(ctx context.Context, src io.Reader) (*buildpb.QueryResult, error) {
	result := &buildpb.QueryResult{
		Target: make([]*buildpb.Target, 0),
	}

	br := bufio.NewReader(src)
	unmarshalOpts := protodelim.UnmarshalOptions{
		MaxSize: 64 * 1024 * 1024, // 64MB limit
	}

	var parseErr error
	for {
		var target buildpb.Target
		err := unmarshalOpts.UnmarshalFrom(br, &target)
		if err == io.EOF {
			break
		}
		if err != nil {
			// Save first error but MUST continue to EOF to drain pipe
			if parseErr == nil {
				parseErr = fmt.Errorf("failed to unmarshal target: %w", err)
			}
			// Continue reading - critical to prevent Bazel from blocking on write
			continue
		}

		// Only collect targets if no error yet
		if parseErr == nil {
			result.Target = append(result.Target, &target)
		}
	}

	return result, parseErr
}
