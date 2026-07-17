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

// Benchmarks for GetChangedTargets against fixed, checked-in commit pairs.
// Each iteration uses a unique pair of commits so every call is a cache miss,
// measuring true cold-path performance without restarting the server.
// Run via `make bench`; not part of `make test` / CI.
package integration_test

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// commitPairs are consecutive commits from the repo history. Each pair
// produces a unique treehash, guaranteeing a cache miss per iteration.
var commitPairs = []struct{ first, second string }{
	{"f0a1fae0786faa4b52cdd0753e93a5e6761fcf6c", "aa03d2c7a2404c88d880a879ca8c44c9fcc75b30"},
	{"aa03d2c7a2404c88d880a879ca8c44c9fcc75b30", "7af31074dfa45a0216f5cc8f1866cecc12708dbc"},
	{"7af31074dfa45a0216f5cc8f1866cecc12708dbc", "124cb6cd0c814007d282981efd79598679b1e6c8"},
	{"124cb6cd0c814007d282981efd79598679b1e6c8", "8116956921fb0dc022150d4c11f3bdd629f8da7c"},
	{"8116956921fb0dc022150d4c11f3bdd629f8da7c", "9a5ddf73c73d5e3e17f07a335ed891c1761ed7f1"},
	{"9a5ddf73c73d5e3e17f07a335ed891c1761ed7f1", "83cfae7cd03f4c09a46ff67a5e4db557e3d52bbd"},
	{"83cfae7cd03f4c09a46ff67a5e4db557e3d52bbd", "1991c274dcd9ba790d883745f0dd6ccd8263dec0"},
	{"1991c274dcd9ba790d883745f0dd6ccd8263dec0", "9f33b50aae84ec5b8cfc796f7405991d98423354"},
	{"9f33b50aae84ec5b8cfc796f7405991d98423354", "461bec17db1142f04ff24a653b7e3020dbda4295"},
	{"461bec17db1142f04ff24a653b7e3020dbda4295", "46b851595b0a1eabfa797bd07fde31cee88c6a1d"},
	{"46b851595b0a1eabfa797bd07fde31cee88c6a1d", "4f246dc46d2d3b2e2bf0126a0908ad7176c07833"},
	{"4f246dc46d2d3b2e2bf0126a0908ad7176c07833", "aa4376860c5601ccfcd3b3e39a27f848acd54814"},
	{"aa4376860c5601ccfcd3b3e39a27f848acd54814", "1d99dd6cbe1749f7ae8239db3b606ac5f298a838"},
	{"1d99dd6cbe1749f7ae8239db3b606ac5f298a838", "16ac2aa8dbe8610f575bce5a5fa6834291379885"},
	{"16ac2aa8dbe8610f575bce5a5fa6834291379885", "82a1e99a8ebf9c993e1416e3a3fd91b254d6ef46"},
}

func BenchmarkGetChangedTargets(b *testing.B) {
	remote := repoRemote(b)
	logger := zap.New(zapcore.NewNopCore())
	addr := startServerWithLogger(b, remote, logger)
	client := newClient(b, addr)

	if b.N > len(commitPairs) {
		b.Fatalf("benchtime=%dx exceeds available commit pairs (%d)", b.N, len(commitPairs))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pair := commitPairs[i]
		getChangedTargets(b, client, remote, pair.first, pair.second)
	}
}
