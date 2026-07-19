package mapper

import (
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/tangopb"
	"go.uber.org/yarpc/encoding/protobuf"
	"go.uber.org/yarpc/yarpcerrors"
)

// ToProtoError converts err into a YARPC error with a TangoError detail.
func ToProtoError(err error) error {
	if err == nil {
		return nil
	}

	tangoCode := tangoerrors.GetErrorCode(err)

	return protobuf.NewError(
		yarpcerrors.CodeInternal, // always return internal because Tango's error codes can't perfectly be translated to yarpc codes, so callers should check the TangoError detail
		err.Error(),
		protobuf.WithErrorDetails(&tangopb.TangoError{
			Code:    toProtoErrorCode(tangoCode),
			Message: err.Error(),
		}),
	)
}

func toProtoErrorCode(code tangoerrors.ErrorCode) tangopb.ErrorCode {
	switch code {
	case tangoerrors.ErrorCancelled:
		return tangopb.ERROR_CANCELLED
	case tangoerrors.ErrorUser:
		return tangopb.ERROR_USER
	case tangoerrors.ErrorInfraRetryable:
		return tangopb.ERROR_INFRA_RETRYABLE
	default:
		return tangopb.ERROR_INFRA
	}
}
