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

package controller

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uber/tango/config"
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/mapper"
	"github.com/uber/tango/mapper/proto"
	"github.com/uber/tango/observability/metrics"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/zap"
)

func testRepositoryID(string) string {
	return "test-repository"
}

func newTestController(logger *zap.Logger) *controller {
	return &controller{
		logger:          logger,
		emitter:         metrics.Nop(),
		maxMessageBytes: config.DefaultMaxMessageBytes,
		appCtx:          context.Background(),
	}
}

type constantGraphConfig struct{ gc config.GraphConfig }

func (c constantGraphConfig) GetGraphConfig(string) (config.GraphConfig, error) { return c.gc, nil }

func staticGraphConfig(gc config.GraphConfig) config.GraphConfigProvider {
	return constantGraphConfig{gc: gc}
}

// newGraphReader builds a storage.GraphReader from entity chunks
// by writing JSON to in-memory storage and reading back.
func newGraphReader(t *testing.T, chunks ...entity.GetTargetGraphResponse) storage.GraphReader {
	t.Helper()
	st := storage.NewMemoryStorage()
	require.NoError(t, storage.WriteGraphStream(t.Context(), st, "test-graph", chunks))
	reader, err := storage.NewGraphReader(t.Context(), st, "test-graph")
	require.NoError(t, err)
	return reader
}

func getTargetGraph(c Controller, request *pb.GetTargetGraphRequest, stream pb.TangoServiceGetTargetGraphYARPCServer) error {
	req, err := mapper.ProtoToGetTargetGraphRequest(request)
	if err != nil {
		return tangoerrors.NewUser(err)
	}
	repo := config.RepositoryConfig{Remote: req.Build.Remote, RepositoryID: testRepositoryID(req.Build.Remote)}
	reader, err := c.GetTargetGraph(stream.Context(), req, repo)
	if err != nil || reader == nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	for {
		if _, err := reader.Read(); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// encodeChangedTargetsChunks gob-encodes a sequence of
// entity.GetChangedTargetsResponse values the way
// storage.WriteChangedTargetsStream does, for use with storage mocks that
// need raw bytes rather than a real backing store.
func encodeChangedTargetsChunks(t *testing.T, chunks []entity.GetChangedTargetsResponse) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	for i := range chunks {
		require.NoError(t, enc.Encode(&chunks[i]))
	}
	return buf.Bytes()
}

// readCloser wraps a string as an io.ReadCloser for storage mock responses.
func readCloser(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

func callGetChangedTargets(c Controller, request *pb.GetChangedTargetsRequest, stream pb.TangoServiceGetChangedTargetsYARPCServer) error {
	req, err := proto.ProtoToGetChangedTargetsRequest(request)
	if err != nil {
		return tangoerrors.NewUser(err)
	}
	repo := config.RepositoryConfig{Remote: req.First.Remote, RepositoryID: testRepositoryID(req.First.Remote)}
	return c.GetChangedTargets(req, request.GetOutputConfig(), stream, repo)
}
