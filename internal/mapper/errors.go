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
		toYARPCCode(tangoCode),
		err.Error(),
		protobuf.WithErrorDetails(&tangopb.TangoError{
			Code:    toProtoErrorCode(tangoCode),
			Message: err.Error(),
		}),
	)
}

func toYARPCCode(code tangoerrors.ErrorCode) yarpcerrors.Code {
	switch code {
	case tangoerrors.ErrorCancelled:
		return yarpcerrors.CodeCancelled
	case tangoerrors.ErrorUser:
		return yarpcerrors.CodeInvalidArgument
	case tangoerrors.ErrorInfra, tangoerrors.ErrorInfraRetryable:
		return yarpcerrors.CodeInternal
	default:
		return yarpcerrors.CodeUnknown
	}
}

func toProtoErrorCode(code tangoerrors.ErrorCode) tangopb.ErrorCode {
	switch code {
	case tangoerrors.ErrorCancelled:
		return tangopb.ERROR_CANCELLED
	case tangoerrors.ErrorUser:
		return tangopb.ERROR_USER
	case tangoerrors.ErrorInfra:
		return tangopb.ERROR_INFRA
	case tangoerrors.ErrorInfraRetryable:
		return tangopb.ERROR_INFRA_RETRYABLE
	default:
		return tangopb.ERROR_UNKNOWN
	}
}
