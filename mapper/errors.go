package mapper

import (
	"context"
	stderrs "errors"

	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/tangopb"
)

func ToProtoError(err error) *tangopb.TangoError {
	if err == nil {
		return nil
	}

	code := tangoerrors.ErrorUnknown
	switch {
	case stderrs.Is(err, context.Canceled):
		code = tangoerrors.ErrorBadRequest
	case stderrs.Is(err, context.DeadlineExceeded):
		code = tangoerrors.ErrorInternal
	default:
		var te *tangoerrors.TangoError
		if stderrs.As(err, &te) {
			code = te.ErrorCode()
		}
	}

	return &tangopb.TangoError{
		Code:    toProtoErrorCode(code),
		Message: err.Error(),
	}
}

func toProtoErrorCode(code tangoerrors.ErrorCode) tangopb.ErrorCode {
	switch code {
	case tangoerrors.ErrorBadRequest:
		return tangopb.ERROR_BAD_REQUEST
	case tangoerrors.ErrorInternal:
		return tangopb.ERROR_INTERNAL
	case tangoerrors.ErrorInternalRetryable:
		return tangopb.ERROR_INTERNAL_RETRYABLE
	default:
		return tangopb.ERROR_UNKNOWN
	}
}
