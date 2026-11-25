package entity

import (
	"time"

	tangoerrors "github.com/uber/tango/core/errors"
)

// JobState is the lifecycle state of an asynchronous job.
type JobState int

const (
	// JobStateInvalid is the zero value. It is not a valid state.
	JobStateInvalid JobState = iota
	// JobStateRunning means a worker is computing the result.
	JobStateRunning
	// JobStateCompleted means the result is available.
	JobStateCompleted
	// JobStateFailed means the computation failed.
	JobStateFailed
)

// String returns the state name.
func (s JobState) String() string {
	switch s {
	case JobStateRunning:
		return "RUNNING"
	case JobStateCompleted:
		return "COMPLETED"
	case JobStateFailed:
		return "FAILED"
	default:
		return "INVALID"
	}
}

// JobStatus is the state of an asynchronous job.
type JobStatus struct {
	State JobState
	// ErrorCode and ErrorMessage describe the failure.
	ErrorCode    tangoerrors.ErrorCode
	ErrorMessage string
	// LastUpdated is the time of the last write. A running job renews it every
	// heartbeat interval. For a COMPLETED or FAILED job it is the end time.
	LastUpdated time.Time
	// RepositoryID is the repository of the job.
	RepositoryID string
	// ResultKey is the storage key of the result. Set only when State is COMPLETED.
	ResultKey string
}
