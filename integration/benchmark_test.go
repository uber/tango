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

// Benchmarks for GetChangedTargets. Run via `make bench`; not part of
// `make test` / `make test-integration` and never from CI.
package integration_test

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// coldCommitPairs are distinct consecutive commit pairs from repo history.
// Each pair produces a unique treehash, guaranteeing a cache miss per call.
var coldCommitPairs = []struct{ first, second string }{
	{"57162624a45965a7e783072c56561f91c5d4084d", "74d1cd55155e5f4f43aa92b4e0146a0c528a0d96"},
	{"74d1cd55155e5f4f43aa92b4e0146a0c528a0d96", "3d54234a3d4c0d940d651e002c5c79d71f01b120"},
	{"3d54234a3d4c0d940d651e002c5c79d71f01b120", "821c885d304811652fefeeeb2c21e1907bebf7f6"},
	{"821c885d304811652fefeeeb2c21e1907bebf7f6", "ea874d203f37c58b9ba52cde81309d1e28827eaa"},
	{"ea874d203f37c58b9ba52cde81309d1e28827eaa", "c0cd90a8c35e2cc981e32f074f9a55657f44d7dd"},
	{"c0cd90a8c35e2cc981e32f074f9a55657f44d7dd", "8d7e7f93ea68bfb5e37f29df289a54ab8ff79ae1"},
	{"8d7e7f93ea68bfb5e37f29df289a54ab8ff79ae1", "4d41a6f57d2215db7e78c284f7f8f5e13f6ff07d"},
	{"4d41a6f57d2215db7e78c284f7f8f5e13f6ff07d", "b4591e33a40135ed5c2fef8ee4f96db8ab231904"},
	{"b4591e33a40135ed5c2fef8ee4f96db8ab231904", "f2b15a5e058ed0a678f97111080806e26d0239d4"},
	{"f2b15a5e058ed0a678f97111080806e26d0239d4", "fc7289244106a1a21a146729c6f75eb6d17f2649"},
	{"3662f8acd9fffb535e56b670beb0de0d811e4181", "381c6585a765e350326520d4e0a588bc604fa656"},
	{"381c6585a765e350326520d4e0a588bc604fa656", "450d404d4a337ec2809f28c630b126871a88987e"},
	{"450d404d4a337ec2809f28c630b126871a88987e", "3a07829426dc621d37bdbe2038ac85a20bec5ead"},
	{"3a07829426dc621d37bdbe2038ac85a20bec5ead", "7876d3870dd0da4ec94cb49362bb85247b373358"},
	{"7876d3870dd0da4ec94cb49362bb85247b373358", "9cc890fb7a91de8b129a3cc86f362b393655de5b"},
}

// BenchmarkGetChangedTargets_Cold measures uncached GetChangedTargets calls.
// Each iteration uses a different commit pair so every call is a cache miss,
// exercising the full pipeline: git checkout, bazel query, compare, stream.
func BenchmarkGetChangedTargets_Cold(b *testing.B) {
	remote := repoRemote(b)
	logger := zap.New(zapcore.NewNopCore())
	addr := startServerWithLogger(b, remote, logger)
	client := newClient(b, addr)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pair := coldCommitPairs[i]
		getChangedTargets(b, client, remote, pair.first, pair.second)
	}
}

// BenchmarkGetChangedTargets_Cached measures cached GetChangedTargets calls.
// The cache is primed with a single call before the timed loop, so every
// iteration in the loop is a cache hit measuring streaming overhead only.
func BenchmarkGetChangedTargets_Cached(b *testing.B) {
	remote := repoRemote(b)
	logger := zap.New(zapcore.NewNopCore())
	addr := startServerWithLogger(b, remote, logger)
	client := newClient(b, addr)

	firstSHA := "57162624a45965a7e783072c56561f91c5d4084d"
	secondSHA := "74d1cd55155e5f4f43aa92b4e0146a0c528a0d96"

	getChangedTargets(b, client, remote, firstSHA, secondSHA)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		getChangedTargets(b, client, remote, firstSHA, secondSHA)
	}
}
