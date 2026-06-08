// Copyright (c) 2025 Uber Technologies, Inc.
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

package cache

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/uber/tango/core/itg/graph"
	"github.com/uber/tango/core/storage"
)

const keyPrefix = "itg/"

// Cache is an interface for caching optimized graphs.
type Cache interface {
	// Put stores an optimized graph in the cache.
	Put(ctx context.Context, optimizedGraph *graph.OptimizedGraph, key Key) error
	// Get retrieves an optimized graph from the cache.
	Get(ctx context.Context, key Key) (*graph.OptimizedGraph, error)
	// FloorKey returns the cache key with the largest commit date that is less than or equal to targetTimeSecond,
	// scoped to the given remote repo.
	FloorKey(ctx context.Context, remote string, targetTimeSecond int64) (Key, error)
}

// Key is a key for a cache entry.
type Key struct {
	Remote               string
	BaseCommitTimeSecond int64
	BaseSha              string
}

// CompareKeyFunc compares two cache keys.
var CompareKeyFunc = func(a Key, b Key) int {
	switch {
	case a.BaseCommitTimeSecond < b.BaseCommitTimeSecond:
		return -1
	case a.BaseCommitTimeSecond > b.BaseCommitTimeSecond:
		return 1
	default:
		return strings.Compare(a.BaseSha, b.BaseSha)
	}
}

// EmptyKey means no cache found.
var EmptyKey = Key{}

// toStorageKey converts a cache key to its storage key: itg/{remote}/{date}/{committime}_{sha}
func (k *Key) toStorageKey() string {
	date := time.Unix(k.BaseCommitTimeSecond, 0).UTC().Format("2006-01-02")
	return filepath.Join(keyPrefix, k.Remote, date, fmt.Sprintf("%d_%s", k.BaseCommitTimeSecond, k.BaseSha))
}

// NewStorageCache creates a new cache backed by a storage.Storage implementation.
// Cache entries are stored under the "itg/" prefix so they can be listed and
// searched independently from other entries in the same storage.
func NewStorageCache(s storage.Storage) Cache {
	return &storageCache{storage: s}
}

type storageCache struct {
	storage storage.Storage
}

func (c *storageCache) Put(ctx context.Context, optimizedGraph *graph.OptimizedGraph, key Key) error {
	exists, err := c.storage.Exists(ctx, key.toStorageKey())
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(optimizedGraph); err != nil {
		return err
	}
	return c.storage.Put(ctx, storage.UploadRequest{Key: key.toStorageKey(), Reader: &buf})
}

func (c *storageCache) Get(ctx context.Context, key Key) (*graph.OptimizedGraph, error) {
	resp, err := c.storage.Get(ctx, storage.DownloadRequest{Key: key.toStorageKey()})
	if err != nil {
		return nil, fmt.Errorf("download graph %s: %w", key.toStorageKey(), err)
	}
	defer resp.ReadCloser.Close()

	var optimizedGraph graph.OptimizedGraph
	if err := gob.NewDecoder(resp.ReadCloser).Decode(&optimizedGraph); err != nil {
		return nil, fmt.Errorf("decode graph %s: %w", key.toStorageKey(), err)
	}
	for _, t := range optimizedGraph.OptimizedTargets {
		if t.Hash == nil {
			t.Hash = []byte{}
		}
	}
	return &optimizedGraph, nil
}

func (c *storageCache) FloorKey(ctx context.Context, remote string, targetTimeSecond int64) (Key, error) {
	remotePrefix := keyPrefix + remote + "/"
	allKeys, err := c.storage.List(ctx, remotePrefix)
	if err != nil {
		return EmptyKey, err
	}

	cacheKeys := make([]Key, 0, len(allKeys))
	for _, k := range allKeys {
		cacheKey, err := parseCacheFileName(strings.TrimPrefix(k, remotePrefix))
		if err != nil {
			continue
		}
		cacheKey.Remote = remote
		cacheKeys = append(cacheKeys, cacheKey)
	}

	if len(cacheKeys) == 0 {
		return EmptyKey, nil
	}

	if !slices.IsSortedFunc(cacheKeys, CompareKeyFunc) {
		slices.SortStableFunc(cacheKeys, CompareKeyFunc)
	}

	if cacheKeys[0].BaseCommitTimeSecond > targetTimeSecond {
		return EmptyKey, nil
	}
	if cacheKeys[len(cacheKeys)-1].BaseCommitTimeSecond <= targetTimeSecond {
		return cacheKeys[len(cacheKeys)-1], nil
	}

	idx, found := binarySearch(cacheKeys, targetTimeSecond)
	if !found {
		idx--
	}
	return cacheKeys[idx], nil
}

func binarySearch(cacheKeys []Key, targetTimeSecond int64) (int, bool) {
	return slices.BinarySearchFunc(cacheKeys, targetTimeSecond, func(a Key, b int64) int {
		switch {
		case a.BaseCommitTimeSecond < b:
			return -1
		case a.BaseCommitTimeSecond > b:
			return 1
		default:
			return 0
		}
	})
}

func parseCacheFileName(name string) (Key, error) {
	// name has the form "date/timestamp_sha" after the keyPrefix+remote are stripped.
	parts := strings.SplitN(name, "/", 2)
	if len(parts) != 2 {
		return Key{}, fmt.Errorf("cache path should have form date/TIMESTAMP_SHA: %s", name)
	}
	filename := parts[1]

	split := strings.SplitN(filename, "_", 2)
	if len(split) != 2 {
		return Key{}, fmt.Errorf("cache file name should have form TIMESTAMP_SHA: %s", filename)
	}

	ts, err := strconv.ParseInt(split[0], 10, 64)
	if err != nil {
		return Key{}, fmt.Errorf("parse timestamp: %w", err)
	}

	return Key{
		BaseCommitTimeSecond: ts,
		BaseSha:              split[1],
	}, nil
}
