// Copyright (c) 2026 Uber Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/streaming"
	"github.com/uber/tango/internal/tgb"
	"github.com/uber/tango/internal/tgbdiff"
)

// ErrCorruptTGB wraps a TGB blob that exists in storage but fails format
// validation (bad magic, truncation, checksum mismatch). Callers should treat
// it as a cache miss — log and recompute, overwriting the bad entry — rather
// than as an infra failure worth failing the request over.
var ErrCorruptTGB = errors.New("corrupt TGB graph blob")

// WriteTGBGraph merges a chunked target graph stream and writes it to storage
// under key as a single TGB blob (see internal/tgb for the format). Unlike
// WriteGraphStream, the payload cannot be produced incrementally — the
// encoder needs the whole graph — but the chunks are already fully
// materialized at every call site, and the encoded blob still streams to
// storage through a pipe rather than being buffered in full.
func WriteTGBGraph(ctx context.Context, st Storage, key string, chunks []entity.GetTargetGraphResponse) error {
	pr, pw := io.Pipe()
	writerErr := make(chan error, 1)
	go func() {
		err := tgbdiff.EncodeChunks(chunks, pw)
		pw.CloseWithError(err)
		writerErr <- err
	}()
	putErr := st.Put(ctx, UploadRequest{Key: key, Reader: pr})
	pr.CloseWithError(putErr)
	encodeErr := <-writerErr
	if putErr != nil {
		return putErr
	}
	if encodeErr != nil {
		return fmt.Errorf("encode TGB graph: %w", encodeErr)
	}
	return nil
}

// TGBGraphReader adapts a stored TGB blob to the chunked GraphReader
// interface, and additionally exposes the underlying random-access
// *tgb.Reader for consumers (the comparison path) that must not pay a full
// decode. Opening validates the blob's header and directory; the full decode
// into entity chunks is deferred to the first Read, so a caller that only
// uses TGB() never pays for it.
type TGBGraphReader struct {
	r        *tgb.Reader
	maxBytes int
	chunks   []entity.GetTargetGraphResponse
	built    bool
	next     int
}

var _ GraphReader = (*TGBGraphReader)(nil)

// NewTGBGraphReader opens the TGB blob at key. A missing blob returns the
// storage backend's not-found error (check with IsNotFound); a blob that
// exists but fails TGB validation returns an error wrapping ErrCorruptTGB.
// maxBytes bounds each decoded chunk's serialized size, mirroring the chunk
// sizing of the gob write path.
func NewTGBGraphReader(ctx context.Context, st Storage, key string, maxBytes int) (*TGBGraphReader, error) {
	resp, err := st.Get(ctx, DownloadRequest{Key: key})
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(resp.ReadCloser)
	closeErr := resp.ReadCloser.Close()
	if err != nil {
		return nil, fmt.Errorf("read TGB blob at %q: %w", key, err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close TGB blob at %q: %w", key, closeErr)
	}
	r, err := tgb.NewReader(data)
	if err != nil {
		return nil, fmt.Errorf("open TGB blob at %q: %w (%w)", key, err, ErrCorruptTGB)
	}
	return &TGBGraphReader{r: r, maxBytes: maxBytes}, nil
}

// TGB returns the underlying random-access reader. It shares the blob bytes
// with this GraphReader and stays valid after Close.
func (g *TGBGraphReader) TGB() *tgb.Reader { return g.r }

// Read returns the next chunk of the decoded graph. The first call decodes
// the whole blob and re-chunks it the way the producer does: target chunks
// first, then metadata chunks, each within maxBytes.
func (g *TGBGraphReader) Read() (entity.GetTargetGraphResponse, error) {
	if !g.built {
		if err := g.build(); err != nil {
			return entity.GetTargetGraphResponse{}, err
		}
	}
	if g.next >= len(g.chunks) {
		return entity.GetTargetGraphResponse{}, io.EOF
	}
	chunk := g.chunks[g.next]
	g.next++
	return chunk, nil
}

// Close releases the decoded chunks. The blob bytes stay reachable through
// any TGB() reader the caller retained.
func (g *TGBGraphReader) Close() error {
	g.chunks = nil
	return nil
}

func (g *TGBGraphReader) build() error {
	graph, err := g.r.DecodeGraph()
	if err != nil {
		return fmt.Errorf("decode TGB graph: %w", err)
	}
	targetGroups, err := streaming.SplitBySize(graph.Targets, g.maxBytes)
	if err != nil {
		return err
	}
	chunks := make([]entity.GetTargetGraphResponse, 0, len(targetGroups))
	for _, group := range targetGroups {
		chunks = append(chunks, entity.GetTargetGraphResponse{Targets: group})
	}
	metaGroups, err := streaming.SplitMetadata(
		graph.Metadata.TargetIDMapping,
		graph.Metadata.RuleTypeMapping,
		graph.Metadata.TagMapping,
		graph.Metadata.AttributeNameMapping,
		graph.Metadata.AttributeStringValueMapping,
		g.maxBytes,
	)
	if err != nil {
		return err
	}
	for _, m := range metaGroups {
		chunks = append(chunks, entity.GetTargetGraphResponse{Metadata: m})
	}
	g.chunks = chunks
	g.built = true
	return nil
}
