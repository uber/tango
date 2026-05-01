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

// decode-graph reads a tango graph storage file and prints it in human-readable JSON.
//
// Usage:
//
//	go run ./tools/decode-graph/main.go <path-to-storage-file>
//
// The file must be in varint-length-delimited protobuf format as written by
// storage.WriteGraphStream. The final message must be a Metadata chunk; it is
// used to resolve numeric IDs back to names for targets, rule types, tags, and
// attributes.
//
// Flags:
//
//	--targets-only   suppress the metadata section in the output
//	--filter <name>  only print targets whose resolved name contains <name>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	gogio "github.com/gogo/protobuf/io"
	pb "github.com/uber/tango/tangopb"
)

func main() {
	targetsOnly := flag.Bool("targets-only", false, "suppress metadata section")
	filter := flag.String("filter", "", "only print targets whose name contains this string")
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "usage: decode-graph [--targets-only] [--filter <name>] <file>\n")
		os.Exit(1)
	}

	f, err := os.Open(flag.Arg(0))
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer f.Close()

	r := gogio.NewDelimitedReader(f, 512<<20)

	var chunks []*pb.GetTargetGraphResponse
	for {
		msg := new(pb.GetTargetGraphResponse)
		if err := r.ReadMsg(msg); err != nil {
			if err == io.EOF {
				break
			}
			log.Fatalf("read message: %v", err)
		}
		if msg.GetItem() == nil {
			break
		}
		chunks = append(chunks, msg)
	}

	if len(chunks) == 0 {
		fmt.Println("(empty file)")
		return
	}

	// The last chunk must be Metadata.
	var meta *pb.Metadata
	if m := chunks[len(chunks)-1].GetMetadata(); m != nil {
		meta = m
		chunks = chunks[:len(chunks)-1]
	}

	// Collect all targets across all chunks.
	var targets []*pb.OptimizedTarget
	for _, c := range chunks {
		if t := c.GetTargets(); t != nil {
			targets = append(targets, t.GetTargets()...)
		}
	}

	type outTarget struct {
		ID           int32             `json:"id"`
		Name         string            `json:"name"`
		Hash         string            `json:"hash,omitempty"`
		RuleType     string            `json:"rule_type,omitempty"`
		Root         bool              `json:"root,omitempty"`
		External     bool              `json:"external,omitempty"`
		Dependencies []string          `json:"dependencies,omitempty"`
		Tags         []string          `json:"tags,omitempty"`
		Attributes   map[string]string `json:"attributes,omitempty"`
	}

	resolve := func(m map[int32]string, id int32) string {
		if m == nil {
			return fmt.Sprintf("%d", id)
		}
		if v, ok := m[id]; ok {
			return v
		}
		return fmt.Sprintf("%d", id)
	}

	var out []outTarget
	for _, t := range targets {
		name := resolve(meta.GetTargetIdMapping(), t.GetId())
		if *filter != "" && !strings.Contains(name, *filter) {
			continue
		}

		deps := make([]string, 0, len(t.GetDirectDependencies()))
		for _, depID := range t.GetDirectDependencies() {
			deps = append(deps, resolve(meta.GetTargetIdMapping(), depID))
		}

		tags := make([]string, 0, len(t.GetTags()))
		for _, tagID := range t.GetTags() {
			tags = append(tags, resolve(meta.GetTagMapping(), tagID))
		}

		attrs := make(map[string]string, len(t.GetAttributes()))
		for nameID, valID := range t.GetAttributes() {
			attrName := resolve(meta.GetAttributeNameMapping(), nameID)
			attrVal := resolve(meta.GetAttributeStringValueMapping(), valID)
			attrs[attrName] = attrVal
		}

		ot := outTarget{
			ID:       t.GetId(),
			Name:     name,
			Hash:     t.GetHash(),
			RuleType: resolve(meta.GetRuleTypeMapping(), t.GetRuleType()),
			Root:     t.GetRoot(),
			External: t.GetExternal(),
		}
		if len(deps) > 0 {
			ot.Dependencies = deps
		}
		if len(tags) > 0 {
			ot.Tags = tags
		}
		if len(attrs) > 0 {
			ot.Attributes = attrs
		}
		out = append(out, ot)
	}

	type metaOut struct {
		UniqueTargets    int `json:"unique_targets"`
		UniqueRuleTypes  int `json:"unique_rule_types"`
		UniqueTags       int `json:"unique_tags"`
		UniqueAttributes int `json:"unique_attribute_names"`
	}

	type output struct {
		TotalTargets int         `json:"total_targets"`
		Targets      []outTarget `json:"targets"`
		Metadata     *metaOut    `json:"metadata,omitempty"`
	}

	result := output{
		TotalTargets: len(targets),
		Targets:      out,
	}
	if !*targetsOnly && meta != nil {
		result.Metadata = &metaOut{
			UniqueTargets:    len(meta.GetTargetIdMapping()),
			UniqueRuleTypes:  len(meta.GetRuleTypeMapping()),
			UniqueTags:       len(meta.GetTagMapping()),
			UniqueAttributes: len(meta.GetAttributeNameMapping()),
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		log.Fatalf("encode JSON: %v", err)
	}
}
