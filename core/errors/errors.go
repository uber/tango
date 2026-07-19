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
	ErrorInfra ErrorCode = iota
	ErrorCancelled
	ErrorUser
	ErrorInfraRetryable
)

// String returns the string form of the code: "cancelled", "user", "infra",
// or "infra_retryable".
func (code ErrorCode) String() string {
	switch code {
	case ErrorInfra:
		return "infra"
	case ErrorCancelled:
		return "cancelled"
	case ErrorUser:
		return "user"
	case ErrorInfraRetryable:
		return "infra_retryable"
	default:
		return "unknown"
	}
}

// TangoError is Tango's internal error type, carrying the underlying error and its `ErrorCode`.
// The `mapper` package uses the error and error code to build the proto `TangoError` for the RPC response, and metrics emitters use the error code as a metric tag.
type TangoError struct {
	err       error
	errorCode ErrorCode
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
func NewInfra(err error) error {
	return newError(err, ErrorInfra)
}

// NewUser wraps err as a TangoError classified ErrorUser.
func NewUser(err error) error {
	return newError(err, ErrorUser)
}

// NewInfraRetryable wraps err as a TangoError classified ErrorInfraRetryable.
func NewInfraRetryable(err error) error {
	return newError(err, ErrorInfraRetryable)
}

func newError(err error, code ErrorCode) error {
	if err == nil {
		return nil
	}

	return &TangoError{
		err:       err,
		errorCode: code,
	}
}

// GetErrorCode extracts the ErrorCode from err.
// If err is context.Canceled, ErrorCancelled is returned.
// If err wraps a TangoError, its code is returned.
// Otherwise ErrorInfra is returned, since an unclassified error is treated as an infra failure.
func GetErrorCode(err error) ErrorCode {
	if stderrs.Is(err, context.Canceled) {
		return ErrorCancelled
	}

	var te *TangoError
	if stderrs.As(err, &te) {
		return te.errorCode
	}

	return ErrorInfra
}

// Fields returns zap fields describing err: the error message and its ErrorCode.
func Fields(err error) []zap.Field {
	return []zap.Field{
		zap.Error(err),
		zap.String("error_code", GetErrorCode(err).String()),
	}
}
