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

package targethasher

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
)

var newHash = sha1.New

const (
	externalWorkspaceFilePrefix  = "@"
	_defaultSourceFileVisibility = "//visibility:private"
)

// cancelCheckInterval is the number of files hashed between cancellation
// checks during a directory walk.
const cancelCheckInterval = 1024

// SourceHasher provides hashes for source nodes in the target graph. These
// can be calculated based on disk contents or form other sources such as a
// vcs system.
type SourceHasher interface {
	HashSourceFile(ctx context.Context, s *buildpb.SourceFile) ([]byte, error)
}

// diskHashHelper is a SourceHasher that provides hashes based on disk
// contents for targets inside the main bazel workspace, and a hash of the
// associated repository rule from the WORKSPACE file for targets from
// external workspaces. knownFileHashes (e.g. from a vcs system) can be
// used in place of generating a hash from disk.
type diskHashHelper struct {
	workspaceroot   string
	knownFileHashes map[string][]byte
}

// noOpHasher
type noOpHasher struct {
}

// Params contains the parameters for creating a new SourceHasher.
type Params struct {
	WorkspaceRoot string
	HashConfig    HashConfig
}

// NewSourceHasher creates a new SourceHasher.
func NewSourceHasher(p Params) SourceHasher {
	return &diskHashHelper{
		workspaceroot:   p.WorkspaceRoot,
		knownFileHashes: p.HashConfig.KnownSourceHashes,
	}
}

// HashSourceFile does a no-op hash for the noOpHasher.
func (hh *noOpHasher) HashSourceFile(_ context.Context, sourceFile *buildpb.SourceFile) ([]byte, error) {
	return nil, nil
}

func (hh *diskHashHelper) HashSourceFile(ctx context.Context, sourceFile *buildpb.SourceFile) ([]byte, error) {
	nonDefaultVisibilities := filterVisibilityLabels(sourceFile.GetVisibilityLabel())
	// The location may look like /foo/decl.go:1:1
	location, _, _ := strings.Cut(sourceFile.GetLocation(), ":")
	// check knownFileHashes for a match, fallback to generating hashes from disk if there is no match
	// or if the file has a non-default visibility set
	if h, ok := hh.knownFileHashes[strings.TrimPrefix(location, filepath.Clean(hh.workspaceroot)+string(filepath.Separator))]; ok && len(nonDefaultVisibilities) == 0 {
		return h, nil
	}

	h := newHash()
	io.WriteString(h, sourceFile.GetName())

	fi, err := os.Stat(location)
	if errors.Is(err, os.ErrNotExist) {
		return h.Sum(nil), nil
	}
	if err != nil {
		return nil, err
	}

	var contentHash hash.Hash
	if fi.IsDir() {
		contentHash, err = hashDir(ctx, location)
	} else {
		contentHash, err = hashFile(location)
	}
	if err != nil {
		return nil, err
	}

	h.Write(contentHash.Sum(nil))

	for _, v := range nonDefaultVisibilities {
		h.Write([]byte(v))
	}

	return h.Sum(nil), nil
}

func hashFile(path string) (hash.Hash, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}

	hash := newHash()
	// Using same SHA1 hashing algorithm as git to ensure file hashes
	// are always the same: https://alblue.bandlem.com/2011/08/git-tip-of-week-objects.html
	hash.Write([]byte(fmt.Sprintf("blob %d\000", fi.Size())))
	if _, err := io.Copy(hash, f); err != nil {
		return nil, err
	}
	return hash, nil
}

func hashDir(ctx context.Context, root string) (hash.Hash, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}

	dirHash := newHash()
	var fileCount int
	walkDirFunc := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.Type().IsRegular() {
			if fileCount%cancelCheckInterval == 0 {
				if err := context.Cause(ctx); err != nil {
					return err
				}
			}
			fileCount++
			fileHash, err := hashFile(path)
			if err != nil {
				return err
			}
			dirHash.Write(fileHash.Sum(nil))
		}
		return nil
	}

	err := filepath.WalkDir(root, walkDirFunc)
	return dirHash, err
}

func filterVisibilityLabels(labels []string) (res []string) {
	for _, v := range labels {
		if v != _defaultSourceFileVisibility {
			res = append(res, v)
		}
	}
	return
}

func externalTargetForRule(t string) string {
	// @workspace//path:target -> //external:workspace
	// @@workspace//path:target -> //external:workspace (bzlmod)
	return externalWorkspaceRulePrefix + strings.TrimLeft(strings.Split(t, "//")[0], "@")
}

type canonicalValueType byte

const (
	canonicalMessage canonicalValueType = iota + 1
	canonicalString
	canonicalBool
	canonicalInt32
	canonicalEnum
	canonicalList
)

type canonicalEncoder struct {
	buffer bytes.Buffer
}

// HashRuleCommon hashes the common elements of a buildpb.Rule using explicit
// field, type, presence, and length framing.
func HashRuleCommon(r *buildpb.Rule, h hash.Hash) {
	var encoder canonicalEncoder
	var rule []byte
	if r != nil {
		rule = encodeRule(r)
	}
	encoder.writeField("rule", canonicalMessage, r != nil, rule)
	_, _ = h.Write(encoder.buffer.Bytes())
}

func encodeRule(r *buildpb.Rule) []byte {
	return encodeMessage("Rule", func(encoder *canonicalEncoder) {
		encoder.writeOptionalString("name", r.Name)
		encoder.writeOptionalString("rule_class", r.RuleClass)

		// Location is machine-local and intentionally excluded.
		ruleAttributes := r.GetAttribute()
		attributes := make([]*buildpb.Attribute, 0, len(ruleAttributes))
		for _, attribute := range ruleAttributes {
			if attribute == nil {
				continue
			}
			// generator_location identifies a macro call site and does not affect
			// the rule's behavior.
			if attribute.GetName() == "generator_location" {
				continue
			}
			attributes = append(attributes, attribute)
		}
		// Bazel rule classes enforce unique attribute names, so sorting by name
		// provides a deterministic order for rule instances.
		sort.Slice(attributes, func(i, j int) bool {
			return attributes[i].GetName() < attributes[j].GetName()
		})
		encodedAttributes := make([][]byte, 0, len(attributes))
		for _, attribute := range attributes {
			encodedAttributes = append(encodedAttributes, encodeAttribute(attribute))
		}
		encoder.writeList("attribute", canonicalMessage, encodedAttributes)
		encoder.writeSortedStrings("rule_input", r.GetRuleInput())
		encoder.writeSortedStrings("rule_output", r.GetRuleOutput())
		encoder.writeSortedStrings("default_setting", r.GetDefaultSetting())

		// Aspects do not appear in the query representation and remain excluded.
		encoder.writeOptionalString("skylark_environment_hash_code", r.SkylarkEnvironmentHashCode)
	})
}

func encodeAttribute(attribute *buildpb.Attribute) []byte {
	if attribute == nil {
		return nil
	}

	return encodeMessage("Attribute", func(encoder *canonicalEncoder) {
		encoder.writeOptionalString("name", attribute.Name)

		// Parseable locations are machine-local and intentionally excluded.
		encoder.writeOptionalBool("explicitly_specified", attribute.ExplicitlySpecified)
		encoder.writeOptionalBool("nodep", attribute.Nodep)
		writeOptionalEnum(encoder, "type", attribute.Type)
		encoder.writeOptionalInt32("int_value", attribute.IntValue)
		encoder.writeOptionalString("string_value", attribute.StringValue)
		encoder.writeOptionalBool("boolean_value", attribute.BooleanValue)
		writeOptionalEnum(encoder, "tristate_value", attribute.TristateValue)
		encoder.writeSortedStrings("string_list_value", attribute.GetStringListValue())
		writeSortedMessages(encoder, "string_dict_value", attribute.GetStringDictValue(), encodeStringDictEntry)
		writeSortedMessages(encoder, "fileset_list_value", attribute.GetFilesetListValue(), encodeFilesetEntry)
		writeSortedMessages(encoder, "label_list_dict_value", attribute.GetLabelListDictValue(), encodeLabelListDictEntry)
		writeSortedMessages(encoder, "string_list_dict_value", attribute.GetStringListDictValue(), encodeStringListDictEntry)
		encoder.writeSortedInt32s("int_list_value", attribute.GetIntListValue())
		writeSortedMessages(encoder, "label_dict_unary_value", attribute.GetLabelDictUnaryValue(), encodeLabelDictUnaryEntry)
		writeSortedMessages(encoder, "label_keyed_string_dict_value", attribute.GetLabelKeyedStringDictValue(), encodeLabelKeyedStringDictEntry)

		// License, deprecated string-dict-unary values, and selector lists retain
		// their existing exclusion from rule hashes.
	})
}

func encodeStringDictEntry(entry *buildpb.StringDictEntry) []byte {
	if entry == nil {
		return nil
	}
	return encodeMessage("StringDictEntry", func(encoder *canonicalEncoder) {
		encoder.writeOptionalString("key", entry.Key)
		encoder.writeOptionalString("value", entry.Value)
	})
}

func encodeFilesetEntry(entry *buildpb.FilesetEntry) []byte {
	if entry == nil {
		return nil
	}
	return encodeMessage("FilesetEntry", func(encoder *canonicalEncoder) {
		encoder.writeOptionalString("source", entry.Source)
		encoder.writeOptionalString("destination_directory", entry.DestinationDirectory)
		encoder.writeOptionalBool("files_present", entry.FilesPresent)
		encoder.writeSortedStrings("file", entry.GetFile())
		encoder.writeSortedStrings("exclude", entry.GetExclude())
		writeOptionalEnum(encoder, "symlink_behavior", entry.SymlinkBehavior)
		encoder.writeOptionalString("strip_prefix", entry.StripPrefix)
	})
}

func encodeLabelListDictEntry(entry *buildpb.LabelListDictEntry) []byte {
	if entry == nil {
		return nil
	}
	return encodeMessage("LabelListDictEntry", func(encoder *canonicalEncoder) {
		encoder.writeOptionalString("key", entry.Key)
		encoder.writeSortedStrings("value", entry.GetValue())
	})
}

func encodeStringListDictEntry(entry *buildpb.StringListDictEntry) []byte {
	if entry == nil {
		return nil
	}
	return encodeMessage("StringListDictEntry", func(encoder *canonicalEncoder) {
		encoder.writeOptionalString("key", entry.Key)
		encoder.writeSortedStrings("value", entry.GetValue())
	})
}

func encodeLabelDictUnaryEntry(entry *buildpb.LabelDictUnaryEntry) []byte {
	if entry == nil {
		return nil
	}
	return encodeMessage("LabelDictUnaryEntry", func(encoder *canonicalEncoder) {
		encoder.writeOptionalString("key", entry.Key)
		encoder.writeOptionalString("value", entry.Value)
	})
}

func encodeLabelKeyedStringDictEntry(entry *buildpb.LabelKeyedStringDictEntry) []byte {
	if entry == nil {
		return nil
	}
	return encodeMessage("LabelKeyedStringDictEntry", func(encoder *canonicalEncoder) {
		encoder.writeOptionalString("key", entry.Key)
		encoder.writeOptionalString("value", entry.Value)
	})
}

func encodeMessage(typeName string, writeFields func(*canonicalEncoder)) []byte {
	var encoder canonicalEncoder
	encoder.writeField("message_type", canonicalString, true, []byte(typeName))
	writeFields(&encoder)
	return encoder.buffer.Bytes()
}

func (e *canonicalEncoder) writeField(name string, valueType canonicalValueType, present bool, payload []byte) {
	e.writeBytes([]byte(name))
	_ = e.buffer.WriteByte(byte(valueType))
	if !present {
		_ = e.buffer.WriteByte(0)
		return
	}
	_ = e.buffer.WriteByte(1)
	e.writeBytes(payload)
}

func (e *canonicalEncoder) writeOptionalString(name string, value *string) {
	if value == nil {
		e.writeField(name, canonicalString, false, nil)
		return
	}
	e.writeField(name, canonicalString, true, []byte(*value))
}

func (e *canonicalEncoder) writeOptionalBool(name string, value *bool) {
	if value == nil {
		e.writeField(name, canonicalBool, false, nil)
		return
	}
	payload := byte(0)
	if *value {
		payload = 1
	}
	e.writeField(name, canonicalBool, true, []byte{payload})
}

func (e *canonicalEncoder) writeOptionalInt32(name string, value *int32) {
	if value == nil {
		e.writeField(name, canonicalInt32, false, nil)
		return
	}
	e.writeField(name, canonicalInt32, true, encodeInt32(*value))
}

func writeOptionalEnum[T ~int32](e *canonicalEncoder, name string, value *T) {
	if value == nil {
		e.writeField(name, canonicalEnum, false, nil)
		return
	}
	e.writeField(name, canonicalEnum, true, encodeInt32(int32(*value)))
}

func (e *canonicalEncoder) writeSortedStrings(name string, values []string) {
	sorted := slices.Clone(values)
	sort.Strings(sorted)
	elements := make([][]byte, 0, len(sorted))
	for _, value := range sorted {
		elements = append(elements, []byte(value))
	}
	e.writeList(name, canonicalString, elements)
}

func (e *canonicalEncoder) writeSortedInt32s(name string, values []int32) {
	sorted := slices.Clone(values)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})
	elements := make([][]byte, 0, len(sorted))
	for _, value := range sorted {
		elements = append(elements, encodeInt32(value))
	}
	e.writeList(name, canonicalInt32, elements)
}

func writeSortedMessages[T any](e *canonicalEncoder, name string, values []*T, encode func(*T) []byte) {
	elements := make([][]byte, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		elements = append(elements, encode(value))
	}
	sortElements(elements)
	e.writeList(name, canonicalMessage, elements)
}

func (e *canonicalEncoder) writeList(name string, elementType canonicalValueType, elements [][]byte) {
	var payload canonicalEncoder
	_ = payload.buffer.WriteByte(byte(elementType))
	payload.writeUvarint(uint64(len(elements)))
	for _, element := range elements {
		_ = payload.buffer.WriteByte(1)
		payload.writeBytes(element)
	}
	e.writeField(name, canonicalList, true, payload.buffer.Bytes())
}

func (e *canonicalEncoder) writeBytes(value []byte) {
	e.writeUvarint(uint64(len(value)))
	_, _ = e.buffer.Write(value)
}

func (e *canonicalEncoder) writeUvarint(value uint64) {
	var encoded [binary.MaxVarintLen64]byte
	length := binary.PutUvarint(encoded[:], value)
	_, _ = e.buffer.Write(encoded[:length])
}

func encodeInt32(value int32) []byte {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], uint32(value))
	return encoded[:]
}

func sortElements(elements [][]byte) {
	sort.Slice(elements, func(i, j int) bool {
		return bytes.Compare(elements[i], elements[j]) < 0
	})
}
