package proto

import (
	tangoerrors "github.com/uber/tango/core/errors"
	"github.com/uber/tango/entity"
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

// JobStatusToProto converts an entity.JobStatus to its proto form.
func JobStatusToProto(status entity.JobStatus) *tangopb.JobStatus {
	pbStatus := &tangopb.JobStatus{State: toProtoJobState(status.State)}
	if status.State == entity.JobStateFailed {
		pbStatus.Error = &tangopb.TangoError{
			Code:    toProtoErrorCode(status.ErrorCode),
			Message: status.ErrorMessage,
		}
	}
	return pbStatus
}

func toProtoJobState(state entity.JobState) tangopb.JobState {
	switch state {
	case entity.JobStateRunning:
		return tangopb.JOB_STATE_RUNNING
	case entity.JobStateCompleted:
		return tangopb.JOB_STATE_COMPLETED
	case entity.JobStateFailed:
		return tangopb.JOB_STATE_FAILED
	default:
		return tangopb.JOB_STATE_INVALID
	}
}
