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

package targethasher

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
	"pgregory.net/rapid"
)

func TestFromProto_queryOrderInvarianceAndInputImmutability(t *testing.T) {
	t.Run("OrderInvariance", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			inputs, knownSourceHashes := drawPermutedQueryResults(t)
			results := make([]normalizedHashResult, len(inputs))
			for i, input := range inputs {
				result, err := FromProto(t.Context(), input, "/workspace", HashConfig{
					KnownSourceHashes: knownSourceHashes,
				})
				if err != nil {
					t.Fatalf("test defect: generated acyclic query variant %d failed: %v", i, err)
				}
				results[i] = normalizeHashResult(result)
			}

			for i := 1; i < len(results); i++ {
				if diff := cmp.Diff(results[0], results[i]); diff != "" {
					t.Fatalf(
						"order instability between variants 0 and %d (-variant 0 +variant %d):\n%s",
						i,
						i,
						diff,
					)
				}
			}
		})
	})

	t.Run("InputImmutability", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			inputs, knownSourceHashes := drawPermutedQueryResults(t)
			var failures []string
			for i, input := range inputs {
				before := proto.Clone(input).(*buildpb.QueryResult)
				_, err := FromProto(t.Context(), input, "/workspace", HashConfig{
					KnownSourceHashes: knownSourceHashes,
				})
				if err != nil {
					t.Fatalf("test defect: generated acyclic query variant %d failed: %v", i, err)
				}
				if !proto.Equal(before, input) {
					failures = append(failures, fmt.Sprintf(
						"input mutation in variant %d (-before +after):\n%s",
						i,
						cmp.Diff(before.String(), input.String()),
					))
				}
			}
			if len(failures) > 0 {
				t.Fatal(strings.Join(failures, "\n"))
			}
		})
	})
}

func drawPermutedQueryResults(t *rapid.T) ([]*buildpb.QueryResult, map[string][]byte) {
	query, knownSourceHashes := drawAcyclicQueryResult(t)
	inputs := []*buildpb.QueryResult{
		proto.Clone(query).(*buildpb.QueryResult),
		proto.Clone(query).(*buildpb.QueryResult),
		proto.Clone(query).(*buildpb.QueryResult),
	}
	permuteQueryResult(t, inputs[1], "first")
	permuteQueryResult(t, inputs[2], "second")
	return inputs, knownSourceHashes
}

func drawAcyclicQueryResult(t *rapid.T) (*buildpb.QueryResult, map[string][]byte) {
	targetCount := rapid.IntRange(1, 8).Draw(t, "targetCount")
	sourceCount := rapid.IntRange(1, min(targetCount, 3)).Draw(t, "sourceCount")

	query := &buildpb.QueryResult{Target: make([]*buildpb.Target, 0, targetCount)}
	knownSourceHashes := make(map[string][]byte, sourceCount)
	targetNames := make([]string, 0, targetCount)
	for i := range sourceCount {
		name := fmt.Sprintf("//pkg:source_%d.txt", i)
		path := fmt.Sprintf("pkg/source_%d.txt", i)
		targetNames = append(targetNames, name)
		knownSourceHashes[path] = []byte(fmt.Sprintf("source-hash-%d", i))
		query.Target = append(query.Target, &buildpb.Target{
			Type: buildpb.Target_SOURCE_FILE.Enum(),
			SourceFile: &buildpb.SourceFile{
				Name:     StringPtr(name),
				Location: StringPtr("/workspace/" + path),
			},
		})
	}

	for i := sourceCount; i < targetCount; i++ {
		name := fmt.Sprintf("//pkg:rule_%d", i)
		var inputs []string
		for dependency, dependencyName := range targetNames {
			if rapid.Bool().Draw(t, fmt.Sprintf("rule-%d-dependency-%d", i, dependency)) {
				inputs = append(inputs, dependencyName)
			}
		}
		tags := rapid.SliceOfNDistinct(
			rapid.SampledFrom([]string{"manual", "small", "test", "local"}),
			0,
			4,
			rapid.ID[string],
		).Draw(t, fmt.Sprintf("rule-%d-tags", i))
		priorities := rapid.SliceOfNDistinct(
			rapid.Int32Range(-3, 3),
			0,
			4,
			rapid.ID[int32],
		).Draw(t, fmt.Sprintf("rule-%d-priorities", i))
		query.Target = append(query.Target, &buildpb.Target{
			Type: buildpb.Target_RULE.Enum(),
			Rule: &buildpb.Rule{
				Name:      StringPtr(name),
				RuleClass: StringPtr("generated_rule"),
				RuleInput: inputs,
				Attribute: []*buildpb.Attribute{
					{
						Name:        StringPtr("description"),
						Type:        buildpb.Attribute_STRING.Enum(),
						StringValue: StringPtr(rapid.StringMatching(`[a-z]{0,8}`).Draw(t, fmt.Sprintf("rule-%d-description", i))),
					},
					{
						Name:            StringPtr("tags"),
						Type:            buildpb.Attribute_STRING_LIST.Enum(),
						StringListValue: tags,
					},
					{
						Name:         StringPtr("priorities"),
						Type:         buildpb.Attribute_INTEGER_LIST.Enum(),
						IntListValue: priorities,
					},
				},
			},
		})
		targetNames = append(targetNames, name)
	}
	return query, knownSourceHashes
}

func permuteQueryResult(t *rapid.T, query *buildpb.QueryResult, variant string) {
	query.Target = rapid.Permutation(query.Target).Draw(t, variant+"-targets")
	for i, target := range query.Target {
		if target.Rule == nil {
			continue
		}
		target.Rule.RuleInput = rapid.Permutation(target.Rule.RuleInput).Draw(t, fmt.Sprintf("%s-rule-%d-inputs", variant, i))
		target.Rule.Attribute = rapid.Permutation(target.Rule.Attribute).Draw(t, fmt.Sprintf("%s-rule-%d-attributes", variant, i))
		for j, attribute := range target.Rule.Attribute {
			attribute.StringListValue = rapid.Permutation(attribute.StringListValue).Draw(t, fmt.Sprintf("%s-rule-%d-attribute-%d-strings", variant, i, j))
			attribute.IntListValue = rapid.Permutation(attribute.IntListValue).Draw(t, fmt.Sprintf("%s-rule-%d-attribute-%d-ints", variant, i, j))
		}
	}
}

type normalizedHashResult struct {
	TargetNames []string
	Targets     []normalizedHashTarget
	Warnings    []string
}

type normalizedHashTarget struct {
	Name            string
	Hash            string
	HashWithoutDeps string
	RuleType        string
	Deps            []string
	Tags            []string
	Root            bool
	External        bool
}

func normalizeHashResult(result Result) normalizedHashResult {
	normalized := normalizedHashResult{
		TargetNames: slices.Clone(result.TargetNames),
		Targets:     make([]normalizedHashTarget, 0, len(result.Targets)),
		Warnings:    make([]string, 0, len(result.Warnings)),
	}
	for _, target := range result.Targets {
		deps := slices.Clone(target.Deps)
		tags := slices.Clone(target.Tags)
		sort.Strings(deps)
		sort.Strings(tags)
		normalized.Targets = append(normalized.Targets, normalizedHashTarget{
			Name:            target.Name,
			Hash:            string(target.Hash),
			HashWithoutDeps: string(target.HashWithoutDeps),
			RuleType:        target.RuleType,
			Deps:            deps,
			Tags:            tags,
			Root:            target.Root,
			External:        target.External,
		})
	}
	for name := range result.Warnings {
		normalized.Warnings = append(normalized.Warnings, name)
	}
	sort.Slice(normalized.Targets, func(i, j int) bool {
		return normalized.Targets[i].Name < normalized.Targets[j].Name
	})
	sort.Strings(normalized.Warnings)
	return normalized
}
