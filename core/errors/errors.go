package errors

import (
	"context"
	stderrs "errors"

	"go.uber.org/zap"
)

// ErrorCode mirrors the proto ErrorCode enum and classifies a TangoError as
// a user error or an infra error (retryable or not).
type ErrorCode int

const (
	ErrorUnknown ErrorCode = iota
	ErrorCancelled
	ErrorUser
	ErrorInfra
	ErrorInfraRetryable
)

// String returns the string form of the code: "cancelled", "user", "infra",
// "infra_retryable", or "unknown" as the default.
func (code ErrorCode) String() string {
	switch code {
	case ErrorCancelled:
		return "cancelled"
	case ErrorUser:
		return "user"
	case ErrorInfra:
		return "infra"
	case ErrorInfraRetryable:
		return "infra_retryable"
	default:
		return "unknown"
	}
}

// FailureSource represents the component where an error occurred.
type FailureSource interface {
	source()
	String() string
}

type source string

func (s source) source()        {}
func (s source) String() string { return string(s) }

var (
	// FailureSourceUnknown is used as a fallback when no other source applies.
	FailureSourceUnknown FailureSource = source("unknown")
	// FailureSourceGit represents failures in git operations.
	FailureSourceGit FailureSource = source("git")
	// FailureSourceBazel represents failures in bazel operations.
	FailureSourceBazel FailureSource = source("bazel")
	// FailureSourceITG represents failures in ITG cache, changeanalyzer, and graph.
	FailureSourceITG FailureSource = source("itg")
	// FailureSourceStorage represents failures in storage.
	FailureSourceStorage FailureSource = source("storage")
	// FailureSourceConfig represents failures in config parser.
	FailureSourceConfig FailureSource = source("config")
	// FailureSourceController represents failures in controller.
	FailureSourceController FailureSource = source("controller")
	// FailureSourceTargetHasher represents failures in target hasher.
	FailureSourceTargetHasher FailureSource = source("targethasher")
	// FailureSourceOrchestrator represents failures in orchestrator.
	FailureSourceOrchestrator FailureSource = source("orchestrator")
	// FailureSourceRepoManager represents failures in repo manager.
	FailureSourceRepoManager FailureSource = source("repomanager")
)

// TangoError is Tango's internal error type, carrying the underlying error, its `FailureSource`, and its `ErrorCode`.
// The `mapper` package uses the error and error code to build the proto `TangoError` for the RPC response, and metrics emitters use the failure source and error code as metric tags.
type TangoError struct {
	failureSource FailureSource
	err           error
	errorCode     ErrorCode
}

// Error returns the underlying error's message.
func (te *TangoError) Error() string {
	return te.err.Error()
}

// Unwrap returns the underlying error, so errors.Is / errors.As can traverse a TangoError.
func (te *TangoError) Unwrap() error {
	return te.err
}

// NewInfra wraps err as a TangoError classified ErrorInfra.
func NewInfra(src FailureSource, err error) error {
	return newError(src, err, ErrorInfra)
}

// NewUser wraps err as a TangoError classified ErrorUser.
func NewUser(src FailureSource, err error) error {
	return newError(src, err, ErrorUser)
}

// NewInfraRetryable wraps err as a TangoError classified ErrorInfraRetryable.
func NewInfraRetryable(src FailureSource, err error) error {
	return newError(src, err, ErrorInfraRetryable)
}

func newError(src FailureSource, err error, code ErrorCode) error {
	if err == nil {
		return nil
	}

	if src == nil {
		src = FailureSourceUnknown
	}

	var te *TangoError
	if stderrs.As(err, &te) {
		src = te.failureSource
		code = te.errorCode
	}

	return &TangoError{
		failureSource: src,
		err:           err,
		errorCode:     code,
	}
}

// GetErrorCode extracts the ErrorCode from err.
// If err is context.Canceled, ErrorCancelled is returned
// Otherwise, if err wraps a TangoError, its code is returned.
// Otherwise ErrorUnknown is returned.
func GetErrorCode(err error) ErrorCode {
	if stderrs.Is(err, context.Canceled) {
		return ErrorCancelled
	}

	var te *TangoError
	if stderrs.As(err, &te) {
		return te.errorCode
	}

	return ErrorUnknown
}

// GetFailureSource extracts the FailureSource from err.
// If err wraps a TangoError, its source is returned.
// Otherwise FailureSourceUnknown is returned.
func GetFailureSource(err error) FailureSource {
	var te *TangoError
	if stderrs.As(err, &te) {
		return te.failureSource
	}

	return FailureSourceUnknown
}

// Fields returns zap fields describing err: the error message, its ErrorCode, and its FailureSource.
func Fields(err error) []zap.Field {
	return []zap.Field{
		zap.Error(err),
		zap.String("error_code", GetErrorCode(err).String()),
		zap.String("failure_source", GetFailureSource(err).String()),
	}
}
