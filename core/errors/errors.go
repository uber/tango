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
)

const (
	ErrorTypeUser  = "user"
	ErrorTypeInfra = "infra"
)

const (
	FailureReasonCancelled        = "cancelled"
	FailureReasonDeadlineExceeded = "deadline_exceeded"
	FailureReasonUnknown          = "unknown"
	FailureReasonStorage          = "storage"
	FailureReasonValidation       = "validation"
)

// ClassifiedError wraps any error with an explicit error type and reason for metrics.
type ClassifiedError struct {
	ErrorType string
	Reason    string
	Err       error
}

func (e *ClassifiedError) Error() string { return e.Err.Error() }
func (e *ClassifiedError) Unwrap() error { return e.Err }

// New constructs a ClassifiedError.
func New(errorType, reason string, err error) *ClassifiedError {
	return &ClassifiedError{ErrorType: errorType, Reason: reason, Err: err}
}

// User Sentinel errors

var (
	ErrRootDirEmpty                  = New(ErrorTypeUser, FailureReasonValidation, stderrors.New("root directory cannot be empty"))
	ErrBuildDescriptionEmpty         = New(ErrorTypeUser, FailureReasonValidation, stderrors.New("build description is empty or invalid"))
	ErrRequestNil                    = New(ErrorTypeUser, FailureReasonValidation, stderrors.New("request cannot be nil"))
	ErrFirstRevisionRequired         = New(ErrorTypeUser, FailureReasonValidation, stderrors.New("first revision is required"))
	ErrSecondRevisionRequired        = New(ErrorTypeUser, FailureReasonValidation, stderrors.New("second revision is required"))
	ErrFirstRevisionRemoteRequired   = New(ErrorTypeUser, FailureReasonValidation, stderrors.New("first revision remote is required"))
	ErrFirstRevisionBaseSHARequired  = New(ErrorTypeUser, FailureReasonValidation, stderrors.New("first revision base_sha is required"))
	ErrSecondRevisionRemoteRequired  = New(ErrorTypeUser, FailureReasonValidation, stderrors.New("second revision remote is required"))
	ErrSecondRevisionBaseSHARequired = New(ErrorTypeUser, FailureReasonValidation, stderrors.New("second revision base_sha is required"))
	ErrRevisionRemoteMismatch        = New(ErrorTypeUser, FailureReasonValidation, stderrors.New("first and second revision must have the same remote"))
)

// Infra Sentinel errors

var (
	ErrParentPackageNotExist        = New(ErrorTypeInfra, FailureReasonUnknown, stderrors.New("parent package does not exist"))
	ErrNilReader                    = New(ErrorTypeInfra, FailureReasonUnknown, stderrors.New("nil reader"))
	ErrRepoManagerClonePathRequired = New(ErrorTypeInfra, FailureReasonUnknown, stderrors.New("service.repo_manager_clone_path must be set when worker_root_path is specified"))
	ErrNoChunksReturned             = New(ErrorTypeInfra, FailureReasonUnknown, stderrors.New("no chunks returned"))
)

// User Structured errors
// Wrap at call site: New(ErrorTypeUser, FailureReasonValidation, &ErrFoo{...})

// ErrRegexPatternInvalid is returned when a regex pattern fails to compile.
type ErrRegexPatternInvalid struct {
	Pattern string
	Cause   error
}

func (e *ErrRegexPatternInvalid) Error() string {
	return fmt.Sprintf("invalid pattern %q: %v", e.Pattern, e.Cause)
}
func (e *ErrRegexPatternInvalid) Unwrap() error { return e.Cause }

// ErrBuildDescriptionMissingFields is returned when required build description fields are absent.
type ErrBuildDescriptionMissingFields struct {
	BaseSha string
	Remote  string
}

func (e *ErrBuildDescriptionMissingFields) Error() string {
	return fmt.Sprintf("build description is missing required fields: base_sha: %s, remote: %s", e.BaseSha, e.Remote)
}

// Infra Structured errors
// Wrap at call site: New(ErrorTypeInfra, FailureReasonUnknown, &ErrFoo{...})

// ErrTargetTypeNotHandled is returned when a buildpb.Target proto has an unexpected type.
type ErrTargetTypeNotHandled struct{ TargetType string }

func (e *ErrTargetTypeNotHandled) Error() string {
	return fmt.Sprintf("cannot handle target type %q", e.TargetType)
}

// ErrExternalRepositoryNotFound is returned when an external repository cannot be resolved.
type ErrExternalRepositoryNotFound struct{ Repo, Target string }

func (e *ErrExternalRepositoryNotFound) Error() string {
	return fmt.Sprintf("cannot find external repository %s from external target %s", e.Repo, e.Target)
}

// ErrUnexpectedRepository is returned when a target references an unexpected repository.
type ErrUnexpectedRepository struct{ Target string }

func (e *ErrUnexpectedRepository) Error() string {
	return fmt.Sprintf("unexpected repository from target %s", e.Target)
}

// ErrDownloadGraph is returned when downloading a cached graph fails.
type ErrDownloadGraph struct {
	Key   string
	Cause error
}

func (e *ErrDownloadGraph) Error() string { return fmt.Sprintf("download graph %s: %v", e.Key, e.Cause) }
func (e *ErrDownloadGraph) Unwrap() error { return e.Cause }

// ErrDecodeGraph is returned when decoding a cached graph fails.
type ErrDecodeGraph struct {
	Key   string
	Cause error
}

func (e *ErrDecodeGraph) Error() string { return fmt.Sprintf("decode graph %s: %v", e.Key, e.Cause) }
func (e *ErrDecodeGraph) Unwrap() error { return e.Cause }

// ErrCachePathInvalid is returned when a cache path does not match the expected format.
type ErrCachePathInvalid struct{ Path string }

func (e *ErrCachePathInvalid) Error() string {
	return fmt.Sprintf("cache path should have form date/TIMESTAMP_SHA: %s", e.Path)
}

// ErrCacheFilenameInvalid is returned when a cache filename does not match the expected format.
type ErrCacheFilenameInvalid struct{ Filename string }

func (e *ErrCacheFilenameInvalid) Error() string {
	return fmt.Sprintf("cache file name should have form TIMESTAMP_SHA: %s", e.Filename)
}

// ErrParseTimestamp is returned when parsing a timestamp from a cache entry fails.
type ErrParseTimestamp struct{ Cause error }

func (e *ErrParseTimestamp) Error() string { return fmt.Sprintf("parse timestamp: %v", e.Cause) }
func (e *ErrParseTimestamp) Unwrap() error { return e.Cause }

// ErrTargetNotFoundInGraph is returned when an expected target is absent from the graph.
type ErrTargetNotFoundInGraph struct{ ID int }

func (e *ErrTargetNotFoundInGraph) Error() string {
	return fmt.Sprintf("target %d not found in graph", e.ID)
}

// ErrTargetMissingHash is returned when a source file or package group target lacks a hash.
type ErrTargetMissingHash struct{ Target string }

func (e *ErrTargetMissingHash) Error() string {
	return fmt.Sprintf("source file or package group target %s should already have hash", e.Target)
}

// ErrRuleTargetMissingHash is returned when a rule target lacks a rule hash.
type ErrRuleTargetMissingHash struct{ Target string }

func (e *ErrRuleTargetMissingHash) Error() string {
	return fmt.Sprintf("rule target %s should already have rule hash", e.Target)
}

// ErrTargetNotFound is returned when an expected target is absent from the graph.
type ErrTargetNotFound struct{ ID int }

func (e *ErrTargetNotFound) Error() string { return fmt.Sprintf("target %d not found", e.ID) }

// ErrDependencyNotFound is returned when a named dependency of a target is not found.
type ErrDependencyNotFound struct{ Dep, Target string }

func (e *ErrDependencyNotFound) Error() string {
	return fmt.Sprintf("dependency %s of target %s not found", e.Dep, e.Target)
}

// ErrDependencyTargetNotFound is returned when a dependency target is absent from the graph.
type ErrDependencyTargetNotFound struct {
	Dep string
	ID  int
}

func (e *ErrDependencyTargetNotFound) Error() string {
	return fmt.Sprintf("dependency target %s (id=%d) not found in graph", e.Dep, e.ID)
}

// ErrUnreachableWorkspaceRoot is returned when the workspace root cannot be reached.
type ErrUnreachableWorkspaceRoot struct{ Root string }

func (e *ErrUnreachableWorkspaceRoot) Error() string {
	return fmt.Sprintf("unable to reach workspace root %q", e.Root)
}

// ErrPRCommitHistory is returned when reading PR commit history fails.
type ErrPRCommitHistory struct{ Cause error }

func (e *ErrPRCommitHistory) Error() string {
	return fmt.Sprintf("failed to read PR commit history: %v", e.Cause)
}
func (e *ErrPRCommitHistory) Unwrap() error { return e.Cause }

// ErrCommitNotAncestor is returned when a commit is not an ancestor of the given PR.
type ErrCommitNotAncestor struct{ Commit, PR string }

func (e *ErrCommitNotAncestor) Error() string {
	return fmt.Sprintf("commit %q is not an ancestor of PR %s", e.Commit, e.PR)
}

// ErrBazeliskHTTPFailure is returned when bazelisk download receives a non-success HTTP response.
type ErrBazeliskHTTPFailure struct {
	StatusCode int
	URL        string
}

func (e *ErrBazeliskHTTPFailure) Error() string {
	return fmt.Sprintf("download bazelisk: HTTP %d from %s", e.StatusCode, e.URL)
}

// ErrBazelQuery is returned when a bazel query fails.
type ErrBazelQuery struct {
	Msg   string
	Tail  string
	Cause error
}

func (e *ErrBazelQuery) Error() string { return fmt.Sprintf("%s: %v%s", e.Msg, e.Cause, e.Tail) }
func (e *ErrBazelQuery) Unwrap() error { return e.Cause }

// ErrCheckRefAncestor is returned when checking git ref ancestry fails.
type ErrCheckRefAncestor struct {
	AncestorRef   string
	DescendantRef string
	Cause         error
}

func (e *ErrCheckRefAncestor) Error() string {
	return fmt.Sprintf("check if ref %s is ancestor of %s: %v", e.AncestorRef, e.DescendantRef, e.Cause)
}
func (e *ErrCheckRefAncestor) Unwrap() error { return e.Cause }

// ErrWorkerPoolSizeInvalid is returned when the configured worker pool size is invalid.
type ErrWorkerPoolSizeInvalid struct{ Value int }

func (e *ErrWorkerPoolSizeInvalid) Error() string {
	return fmt.Sprintf("service.worker_pool_size must be > 0, got %d", e.Value)
}

// ErrRepositoryRemoteEmpty is returned when a repository entry has an empty remote.
type ErrRepositoryRemoteEmpty struct{ Index int }

func (e *ErrRepositoryRemoteEmpty) Error() string {
	return fmt.Sprintf("repository[%d].remote must not be empty", e.Index)
}

// ErrDuplicateRemote is returned when two repository entries share the same remote.
type ErrDuplicateRemote struct{ Remote string }

func (e *ErrDuplicateRemote) Error() string {
	return fmt.Sprintf("duplicate repository remote %q", e.Remote)
}

// ErrGetTargetGraph is returned when fetching a numbered target graph fails.
type ErrGetTargetGraph struct {
	Order int
	Cause error
}

func (e *ErrGetTargetGraph) Error() string {
	return fmt.Sprintf("failed to get target graph #%d: %v", e.Order, e.Cause)
}
func (e *ErrGetTargetGraph) Unwrap() error { return e.Cause }

// ErrTargetIDNotInMetadata is returned when a target ID is absent from the metadata map.
// Role identifies which target (e.g. "current", "old", "new").
type ErrTargetIDNotInMetadata struct {
	ID   uint32
	Role string
}

func (e *ErrTargetIDNotInMetadata) Error() string {
	return fmt.Sprintf("%s target id %d not found in metadata", e.Role, e.ID)
}

// ErrTargetNamesMismatch is returned when old and new target names disagree.
type ErrTargetNamesMismatch struct{ OldName, NewName string }

func (e *ErrTargetNamesMismatch) Error() string {
	return fmt.Sprintf("target names are different %s != %s", e.OldName, e.NewName)
}

// ErrTreehashRead is returned when reading a treehash from storage fails.
type ErrTreehashRead struct {
	Key   string
	Cause error
}

func (e *ErrTreehashRead) Error() string {
	return fmt.Sprintf("treehash read failed for key %q: %v", e.Key, e.Cause)
}
func (e *ErrTreehashRead) Unwrap() error { return e.Cause }

// ErrTreehashBodyRead is returned when reading the body of a treehash response fails.
type ErrTreehashBodyRead struct {
	Key   string
	Cause error
}

func (e *ErrTreehashBodyRead) Error() string {
	return fmt.Sprintf("treehash body read failed for key %q: %v", e.Key, e.Cause)
}
func (e *ErrTreehashBodyRead) Unwrap() error { return e.Cause }

// ErrNoRepositoryConfig is returned when no repository configuration exists for a remote.
type ErrNoRepositoryConfig struct{ Remote string }

func (e *ErrNoRepositoryConfig) Error() string {
	return fmt.Sprintf("no repository configuration found for remote %q", e.Remote)
}
