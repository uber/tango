package errors

import (
	stderrs "errors"
)

// ErrorCode classifies a TangoError for proto mapping.
type ErrorCode int

const (
	ErrorUnknown ErrorCode = iota
	ErrorBadRequest
	ErrorInternal
	ErrorInternalRetryable
)

// FailureSource identifies the subsystem where an error originated.
type FailureSource interface {
	source()
	String() string
}

type source string

func (s source) source()        {}
func (s source) String() string { return string(s) }

var (
	FailureSourceUnknown      FailureSource = source("unknown")
	FailureSourceGit          FailureSource = source("git")
	FailureSourceBazel        FailureSource = source("bazel")
	FailureSourceITG          FailureSource = source("itg")
	FailureSourceStorage      FailureSource = source("storage")
	FailureSourceWorkspace    FailureSource = source("workspace")
	FailureSourceConfig       FailureSource = source("config")
	FailureSourceTargetHasher FailureSource = source("targethasher")
	FailureSourceOrchestrator FailureSource = source("orchestrator")
	FailureSourceRepoManager  FailureSource = source("repomanager")
)

// TangoError is an error with classification for metrics and proto mapping.
type TangoError struct {
	failureSource FailureSource
	err           error
	errorCode     ErrorCode
}

func (te *TangoError) Error() string {
	return te.err.Error()
}

func (te *TangoError) Unwrap() error {
	return te.err
}

func (te *TangoError) ErrorCode() ErrorCode {
	return te.errorCode
}

func (te *TangoError) FailureSource() FailureSource {
	return te.failureSource
}

func NewInternal(src FailureSource, err error) error {
	return newError(src, err, ErrorInternal)
}

func NewUser(src FailureSource, err error) error {
	return newError(src, err, ErrorBadRequest)
}

func NewInternalRetryable(src FailureSource, err error) error {
	return newError(src, err, ErrorInternalRetryable)
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
		src = te.FailureSource()
		code = te.ErrorCode()
	}

	return &TangoError{
		failureSource: src,
		err:           err,
		errorCode:     code,
	}
}
