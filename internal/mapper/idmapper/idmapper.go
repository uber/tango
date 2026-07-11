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

// Package idmapper provides the ID-mapping primitives used to build the tangopb
// wire format's int32-ID-keyed maps.
package idmapper

// Mapper assigns stable int32 IDs to string names on demand.
// IDs are assigned sequentially starting from 1. Zero is reserved as the
// proto3 "unset" sentinel so consumers using encoding/json (which honors
// `omitempty` on int32 fields) or any client that treats GetId() == 0 as
// missing never silently lose real entries.
type Mapper struct {
	nameToID map[string]int32
	nextID   int32
}

// NewMapper creates a new Mapper.
func NewMapper() *Mapper {
	return &Mapper{
		nameToID: make(map[string]int32),
		nextID:   1,
	}
}

// ID returns the existing ID for the provided name or assigns a new one.
func (a *Mapper) ID(name string) int32 {
	if id, ok := a.nameToID[name]; ok {
		return id
	}
	id := a.nextID
	a.nextID++
	a.nameToID[name] = id
	return id
}

// Invert returns an id->name map built from the current name->id map.
func (a *Mapper) Invert() map[int32]string {
	out := make(map[int32]string, len(a.nameToID))
	for name, id := range a.nameToID {
		out[id] = name
	}
	return out
}
