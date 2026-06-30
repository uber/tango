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

package errors

import (
	stderrors "errors"
	"fmt"
	"testing"
)

func TestNew(t *testing.T) {
	inner := stderrors.New("something went wrong")
	ce := New(ErrorTypeInfra, FailureReasonUnknown, inner)

	if ce.ErrorType != ErrorTypeInfra {
		t.Errorf("ErrorType = %q, want %q", ce.ErrorType, ErrorTypeInfra)
	}
	if ce.Reason != FailureReasonUnknown {
		t.Errorf("Reason = %q, want %q", ce.Reason, FailureReasonUnknown)
	}
	if ce.Error() != inner.Error() {
		t.Errorf("Error() = %q, want %q", ce.Error(), inner.Error())
	}
	if stderrors.Unwrap(ce) != inner {
		t.Error("Unwrap() did not return the inner error")
	}
}

func TestClassifiedError_AsTraversal(t *testing.T) {
	// errors.As should find *ClassifiedError through a wrapping fmt.Errorf.
	inner := stderrors.New("root cause")
	ce := New(ErrorTypeUser, FailureReasonValidation, inner)
	wrapped := fmt.Errorf("outer: %w", ce)

	var found *ClassifiedError
	if !stderrors.As(wrapped, &found) {
		t.Fatal("errors.As did not find *ClassifiedError in chain")
	}
	if found.ErrorType != ErrorTypeUser {
		t.Errorf("ErrorType = %q, want %q", found.ErrorType, ErrorTypeUser)
	}
}

func TestClassifiedError_StructuredInnerAsTraversal(t *testing.T) {
	// errors.As should traverse ClassifiedError to find the inner structured type.
	inner := &ErrDownloadGraph{Key: "itg/abc", Cause: stderrors.New("io error")}
	ce := New(ErrorTypeInfra, FailureReasonUnknown, inner)

	var found *ErrDownloadGraph
	if !stderrors.As(ce, &found) {
		t.Fatal("errors.As did not find *ErrDownloadGraph through *ClassifiedError")
	}
	if found.Key != "itg/abc" {
		t.Errorf("Key = %q, want %q", found.Key, "itg/abc")
	}
}

func TestClassifiedError_IsTraversal(t *testing.T) {
	// errors.Is should traverse ClassifiedError and into the structured type's Cause.
	root := stderrors.New("root cause")
	inner := &ErrDownloadGraph{Key: "k", Cause: root}
	ce := New(ErrorTypeInfra, FailureReasonUnknown, inner)

	if !stderrors.Is(ce, root) {
		t.Error("errors.Is did not find root cause through *ClassifiedError and *ErrDownloadGraph")
	}
}

func TestSentinelVars(t *testing.T) {
	tests := []struct {
		name      string
		sentinel  *ClassifiedError
		wantType  string
		wantMsg   string
	}{
		{"ErrRootDirEmpty", ErrRootDirEmpty, ErrorTypeUser, "root directory cannot be empty"},
		{"ErrRequestNil", ErrRequestNil, ErrorTypeUser, "request cannot be nil"},
		{"ErrNilReader", ErrNilReader, ErrorTypeInfra, "nil reader"},
		{"ErrNoChunksReturned", ErrNoChunksReturned, ErrorTypeInfra, "no chunks returned"},
		{"ErrParentPackageNotExist", ErrParentPackageNotExist, ErrorTypeInfra, "parent package does not exist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.sentinel.ErrorType != tt.wantType {
				t.Errorf("ErrorType = %q, want %q", tt.sentinel.ErrorType, tt.wantType)
			}
			if tt.sentinel.Error() != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", tt.sentinel.Error(), tt.wantMsg)
			}
		})
	}
}

func TestSentinelIdentity(t *testing.T) {
	// errors.Is on a sentinel should match by pointer identity.
	if !stderrors.Is(ErrRootDirEmpty, ErrRootDirEmpty) {
		t.Error("errors.Is(ErrRootDirEmpty, ErrRootDirEmpty) should be true")
	}
	if stderrors.Is(ErrRootDirEmpty, ErrRequestNil) {
		t.Error("errors.Is(ErrRootDirEmpty, ErrRequestNil) should be false")
	}
}

func TestStructuredErrorStrings(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantMsg string
	}{
		{
			"ErrTargetTypeNotHandled",
			&ErrTargetTypeNotHandled{TargetType: "UNKNOWN"},
			`cannot handle target type "UNKNOWN"`,
		},
		{
			"ErrExternalRepositoryNotFound",
			&ErrExternalRepositoryNotFound{Repo: "myrepo", Target: "//ext:target"},
			"cannot find external repository myrepo from external target //ext:target",
		},
		{
			"ErrDownloadGraph",
			&ErrDownloadGraph{Key: "itg/k", Cause: stderrors.New("eof")},
			"download graph itg/k: eof",
		},
		{
			"ErrTargetNotFound",
			&ErrTargetNotFound{ID: 42},
			"target 42 not found",
		},
		{
			"ErrTargetNotFoundInGraph",
			&ErrTargetNotFoundInGraph{ID: 7},
			"target 7 not found in graph",
		},
		{
			"ErrDependencyNotFound",
			&ErrDependencyNotFound{Dep: "//foo:bar", Target: "//baz:qux"},
			"dependency //foo:bar of target //baz:qux not found",
		},
		{
			"ErrNoRepositoryConfig",
			&ErrNoRepositoryConfig{Remote: "github.com/uber/tango"},
			`no repository configuration found for remote "github.com/uber/tango"`,
		},
		{
			"ErrTargetIDNotInMetadata_current",
			&ErrTargetIDNotInMetadata{ID: 99, Role: "current"},
			"current target id 99 not found in metadata",
		},
		{
			"ErrBazeliskHTTPFailure",
			&ErrBazeliskHTTPFailure{StatusCode: 403, URL: "https://example.com"},
			"download bazelisk: HTTP 403 from https://example.com",
		},
		{
			"ErrParseTimestamp",
			&ErrParseTimestamp{Cause: stderrors.New("invalid syntax")},
			"parse timestamp: invalid syntax",
		},
		{
			"ErrPRCommitHistory",
			&ErrPRCommitHistory{Cause: stderrors.New("network error")},
			"failed to read PR commit history: network error",
		},
		{
			"ErrCommitNotAncestor",
			&ErrCommitNotAncestor{Commit: "abc123", PR: "456"},
			`commit "abc123" is not an ancestor of PR 456`,
		},
		{
			"ErrRegexPatternInvalid",
			&ErrRegexPatternInvalid{Pattern: "[bad", Cause: stderrors.New("missing closing ]")},
			`invalid pattern "[bad": missing closing ]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
		})
	}
}
